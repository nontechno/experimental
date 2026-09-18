package boltfs

import (
	"errors"
	"io"
	"os"
	"sync"
	"syscall"

	"github.com/go-git/go-billy/v5"
	bolt "go.etcd.io/bbolt"
)

// file is an open handle.
//
// Reads are served from the snapshot taken when the file was opened. Each
// Write and Truncate is an immediate read-modify-write of the whole value
// inside one bbolt transaction, so nothing is buffered, Close cannot fail
// with lost data, and a crash can never leave a partly written value.
//
// go-nfs opens a fresh handle for every READ and WRITE RPC, so snapshots are
// short-lived. A value is rewritten in full on every write, which is what
// makes this suitable for configuration-sized files rather than bulk data.
type file struct {
	fs    *FS
	name  string
	elems []string
	flag  int

	mu     sync.Mutex
	data   []byte
	pos    int64
	closed bool
}

var _ billy.File = (*file)(nil)

func (f *FS) newFile(name string, elems []string, flag int, data []byte) *file {
	return &file{fs: f, name: name, elems: elems, flag: flag, data: data}
}

// Name returns the path the file was opened with; go-nfs passes it back to
// Stat after CREATE.
func (h *file) Name() string { return h.name }

func (h *file) readable() bool { return h.flag&os.O_WRONLY == 0 }
func (h *file) writable() bool { return h.flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND) != 0 }

func (h *file) Read(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n, err := h.readAt(p, h.pos)
	h.pos += int64(n)
	return n, err
}

func (h *file) ReadAt(p []byte, off int64) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.readAt(p, off)
}

func (h *file) readAt(p []byte, off int64) (int, error) {
	switch {
	case h.closed:
		return 0, os.ErrClosed
	case !h.readable():
		return 0, pathErr("read", h.name, syscall.EBADF)
	case off < 0:
		return 0, pathErr("read", h.name, syscall.EINVAL)
	case off >= int64(len(h.data)):
		return 0, io.EOF
	}
	n := copy(p, h.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (h *file) Seek(offset int64, whence int) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, os.ErrClosed
	}
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = h.pos
	case io.SeekEnd:
		base = int64(len(h.data))
	default:
		return 0, pathErr("seek", h.name, syscall.EINVAL)
	}
	if base+offset < 0 {
		return 0, pathErr("seek", h.name, syscall.EINVAL)
	}
	h.pos = base + offset
	return h.pos, nil
}

func (h *file) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.closed:
		return 0, os.ErrClosed
	case !h.writable():
		return 0, pathErr("write", h.name, syscall.EBADF)
	}
	appendMode := h.flag&os.O_APPEND != 0
	var end int64
	data, err := h.fs.rewrite(h.name, h.elems, func(old []byte) ([]byte, error) {
		off := h.pos
		if appendMode {
			off = int64(len(old))
		}
		size := max(int64(len(old)), off+int64(len(p)))
		if size > h.fs.opts.MaxFileSize {
			return nil, &tooLargeError{what: "file", size: size, limit: h.fs.opts.MaxFileSize, flag: "-max-file-size"}
		}
		buf := make([]byte, size)
		copy(buf, old)
		copy(buf[off:], p)
		end = off + int64(len(p))
		return buf, nil
	})
	if err != nil {
		return 0, err
	}
	h.data, h.pos = data, end
	return len(p), nil
}

func (h *file) Truncate(size int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.closed:
		return os.ErrClosed
	case size < 0:
		return pathErr("truncate", h.name, syscall.EINVAL)
	case size > h.fs.opts.MaxFileSize:
		return pathErr("truncate", h.name,
			&tooLargeError{what: "file", size: size, limit: h.fs.opts.MaxFileSize, flag: "-max-file-size"})
	}
	data, err := h.fs.rewrite(h.name, h.elems, func(old []byte) ([]byte, error) {
		if int64(len(old)) == size {
			return nil, errNoChange
		}
		buf := make([]byte, size)
		copy(buf, old)
		return buf, nil
	})
	if err != nil {
		return err
	}
	h.data = data
	return nil
}

func (h *file) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return os.ErrClosed
	}
	h.closed, h.data = true, nil
	return nil
}

// Lock and Unlock are advisory no-ops: NLM (NFSv3 locking) is not served, so
// clients must mount with nolock/nolocks.
func (h *file) Lock() error   { return nil }
func (h *file) Unlock() error { return nil }

var errNoChange = errors.New("no change")

// rewrite applies mutate to a file's value inside one write transaction and
// returns the value as stored.
func (f *FS) rewrite(name string, elems []string, mutate func(old []byte) ([]byte, error)) ([]byte, error) {
	if err := f.writable("write", name); err != nil {
		return nil, err
	}
	var out []byte
	err := f.db.Update(func(tx *bolt.Tx) error {
		d, key, err := parentOf(tx, f.chain(elems))
		if err != nil {
			return err
		}
		k, v := lookup(d, key)
		switch k {
		case kindNone:
			return pathErr("write", name, os.ErrNotExist)
		case kindDir:
			return pathErr("write", name, syscall.EISDIR)
		}
		buf, err := mutate(v)
		if errors.Is(err, errNoChange) {
			out = copyBytes(v)
			return nil
		}
		if err != nil {
			return pathErr("write", name, err)
		}
		if err := d.put(key, buf); err != nil {
			return err
		}
		out = buf
		f.touch(elems)
		return nil
	})
	if err != nil {
		return nil, wrapErr("write", name, err)
	}
	return out, nil
}
