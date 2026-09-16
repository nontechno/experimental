package etcdfs

import (
	"io"
	"os"
	"sync"
	"syscall"

	"github.com/go-git/go-billy/v5"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// file is an open handle.
//
// Reads are served from the value snapshot taken at open. Every Write and
// Truncate is an immediate compare-and-swap rewrite of the whole value, so
// there is no buffered state and Close has nothing to flush. go-nfs opens a
// fresh handle per READ and WRITE RPC, so snapshots are short-lived.
type file struct {
	fs   *FS
	name string // as passed to OpenFile; go-nfs stats it after create
	rel  string
	flag int

	mu     sync.Mutex
	data   []byte
	pos    int64
	closed bool
}

var _ billy.File = (*file)(nil)

func (f *FS) newFile(name, rel string, flag int, data []byte) *file {
	return &file{fs: f, name: name, rel: rel, flag: flag, data: data}
}

func (h *file) Name() string { return h.name }

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
	case h.flag&os.O_WRONLY != 0:
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
	case h.flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND) == 0:
		return 0, pathErr("write", h.name, syscall.EBADF)
	}
	end := int64(0)
	data, err := h.fs.update(h.name, h.rel, func(old []byte) ([]byte, error) {
		off := h.pos
		if h.flag&os.O_APPEND != 0 {
			off = int64(len(old))
		}
		size := max(int64(len(old)), off+int64(len(p)))
		if size > int64(h.fs.opts.MaxFileSize) {
			return nil, syscall.EFBIG
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
	case size > int64(h.fs.opts.MaxFileSize):
		return pathErr("truncate", h.name, syscall.EFBIG)
	}
	data, err := h.fs.update(h.name, h.rel, func(old []byte) ([]byte, error) {
		if int64(len(old)) == size {
			return old, nil
		}
		buf := make([]byte, size)
		copy(buf, old)
		return buf, nil
	})
	if err == nil {
		h.data = data
	}
	return err
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

// Lock and Unlock are no-ops: NLM locking is not served (mount with nolock).
func (h *file) Lock() error   { return nil }
func (h *file) Unlock() error { return nil }

// unchanged reports whether mutate returned the old value itself, meaning
// there is nothing to write.
func unchanged(buf, old []byte) bool {
	return len(buf) == len(old) && (len(buf) == 0 || &buf[0] == &old[0])
}

// update applies mutate to the current value of file rel with a
// compare-and-swap, retrying on concurrent modification, and returns the
// value as stored. mutate may run several times; returning old unchanged
// skips the write.
func (f *FS) update(name, rel string, mutate func(old []byte) ([]byte, error)) ([]byte, error) {
	const op = "write"
	if f.opts.ReadOnly {
		return nil, pathErr(op, name, syscall.EROFS)
	}
	ctx, cancel := f.ctx()
	defer cancel()
	key := f.fileKey(rel)

	var stored []byte
	err := retry(ctx, op, name, func() (bool, error) {
		e, err := f.lookup(ctx, rel)
		switch {
		case err != nil:
			return false, pathErr(op, name, err)
		case !e.exists:
			return false, pathErr(op, name, os.ErrNotExist)
		case e.isDir:
			return false, pathErr(op, name, syscall.EISDIR)
		}
		buf, err := mutate(e.kv.Value)
		if err != nil {
			return false, pathErr(op, name, err)
		}
		if unchanged(buf, e.kv.Value) {
			stored = buf
			return true, nil
		}
		ok, err := f.commit(ctx,
			[]clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(key), "=", e.kv.ModRevision)},
			clientv3.OpPut(key, string(buf), withLease(e.kv)...))
		if err != nil {
			return false, pathErr(op, name, err)
		}
		stored = buf
		return ok, nil
	})
	return stored, err
}
