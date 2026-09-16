package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/pkg/sftp"
)

// openFile is a remote file handle that may be shared by several NFS requests.
//
// go-nfs opens, seeks, reads or writes, and closes the file for *every* READ
// and WRITE RPC. Doing a real SFTP open/close each time would add two round
// trips per 128 KiB of data, so handles are pooled here and closed only after
// they have been idle for a while.
type openFile struct {
	key      string
	path     string
	f        *sftp.File
	gen      uint64
	writable bool
	pooled   bool

	refs int
	last time.Time
	dead bool
}

type fileCache struct {
	mu      sync.Mutex
	entries map[string]*openFile
	max     int
	idle    time.Duration
	stop    chan struct{}
	once    sync.Once
}

func newFileCache(max int, idle time.Duration) *fileCache {
	c := &fileCache{
		entries: make(map[string]*openFile),
		max:     max,
		idle:    idle,
		stop:    make(chan struct{}),
	}
	if idle > 0 {
		go c.janitor()
	}
	return c
}

func cacheKey(remotePath string, writable bool) string {
	if writable {
		return remotePath + "\x00rw"
	}
	return remotePath + "\x00ro"
}

// acquire returns a handle for remotePath, reusing a pooled one when possible.
// Handles opened with creation flags are never pooled, because their flags
// carry semantics that must not be replayed.
func (c *fileCache) acquire(cl *sftp.Client, gen uint64, remotePath string, flag int, pool bool) (*openFile, error) {
	writable := flag&(os.O_WRONLY|os.O_RDWR) != 0
	special := flag&(os.O_CREATE|os.O_TRUNC|os.O_EXCL|os.O_APPEND) != 0
	pool = pool && !special

	if pool {
		key := cacheKey(remotePath, writable)
		var stale *sftp.File
		c.mu.Lock()
		if e, ok := c.entries[key]; ok && !e.dead {
			if e.gen == gen {
				e.refs++
				e.last = time.Now()
				c.mu.Unlock()
				return e, nil
			}
			// Stale session: drop it and open a fresh handle.
			delete(c.entries, key)
			e.dead = true
			if e.refs == 0 {
				stale = e.f
			}
		}
		c.mu.Unlock()
		if stale != nil {
			// Never close a remote handle while holding the cache lock: it is
			// a network round trip and would stall every other file operation.
			stale.Close()
		}
	}

	openFlag := flag
	if pool {
		// Normalise so a pooled handle can serve any compatible request.
		if writable {
			openFlag = os.O_RDWR
		} else {
			openFlag = os.O_RDONLY
		}
	}
	f, err := cl.OpenFile(remotePath, openFlag)
	if err != nil {
		return nil, err
	}

	e := &openFile{
		key:      cacheKey(remotePath, writable),
		path:     remotePath,
		f:        f,
		gen:      gen,
		writable: writable,
		pooled:   pool,
		refs:     1,
		last:     time.Now(),
	}
	if !pool {
		return e, nil
	}

	c.mu.Lock()
	var toClose []*sftp.File
	if old, ok := c.entries[e.key]; ok {
		old.dead = true
		if old.refs == 0 {
			toClose = append(toClose, old.f)
		}
	}
	c.entries[e.key] = e
	toClose = append(toClose, c.evictLocked()...)
	c.mu.Unlock()
	for _, f := range toClose {
		f.Close()
	}
	return e, nil
}

func (c *fileCache) release(e *openFile) error {
	if e == nil {
		return nil
	}
	if !e.pooled {
		return e.f.Close()
	}
	c.mu.Lock()
	e.refs--
	e.last = time.Now()
	dead := e.dead && e.refs <= 0
	if dead {
		if cur, ok := c.entries[e.key]; ok && cur == e {
			delete(c.entries, e.key)
		}
	}
	c.mu.Unlock()
	if dead {
		return e.f.Close()
	}
	return nil
}

// drop forgets any pooled handles for a path, e.g. after it was removed,
// renamed or truncated behind our back.
func (c *fileCache) drop(remotePath string) {
	c.mu.Lock()
	var toClose []*sftp.File
	for _, writable := range []bool{false, true} {
		key := cacheKey(remotePath, writable)
		if e, ok := c.entries[key]; ok {
			delete(c.entries, key)
			e.dead = true
			if e.refs == 0 {
				toClose = append(toClose, e.f)
			}
		}
	}
	c.mu.Unlock()
	for _, f := range toClose {
		f.Close()
	}
}

// dropAll invalidates every pooled handle, used after the SSH session died.
func (c *fileCache) dropAll() {
	c.mu.Lock()
	var toClose []*sftp.File
	for key, e := range c.entries {
		delete(c.entries, key)
		e.dead = true
		if e.refs == 0 {
			toClose = append(toClose, e.f)
		}
	}
	c.mu.Unlock()
	for _, f := range toClose {
		f.Close()
	}
}

// evictLocked trims the pool to its limit and returns the handles the caller
// must close after releasing the lock.
func (c *fileCache) evictLocked() []*sftp.File {
	var toClose []*sftp.File
	for len(c.entries) > c.max {
		var oldest *openFile
		for _, e := range c.entries {
			if e.refs != 0 {
				continue
			}
			if oldest == nil || e.last.Before(oldest.last) {
				oldest = e
			}
		}
		if oldest == nil {
			break // everything is in use; the limit is advisory
		}
		delete(c.entries, oldest.key)
		oldest.dead = true
		toClose = append(toClose, oldest.f)
	}
	return toClose
}

func (c *fileCache) janitor() {
	interval := c.idle / 2
	if interval < time.Second {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case now := <-t.C:
			c.mu.Lock()
			var toClose []*sftp.File
			for key, e := range c.entries {
				if e.refs == 0 && now.Sub(e.last) > c.idle {
					delete(c.entries, key)
					e.dead = true
					toClose = append(toClose, e.f)
				}
			}
			c.mu.Unlock()
			for _, f := range toClose {
				f.Close()
			}
		}
	}
}

func (c *fileCache) Close() {
	c.once.Do(func() { close(c.stop) })
	c.dropAll()
}

// File is the billy.File implementation handed to go-nfs. Each File keeps its
// own offset and performs positional I/O, so several of them can share one
// pooled SFTP handle safely.
type File struct {
	fs     *FS
	name   string
	remote string
	entry  *openFile

	mu       sync.Mutex
	offset   int64
	closed   bool
	writable bool
}

func (f *File) Name() string { return f.name }

func (f *File) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	n, err := f.entry.f.ReadAt(p, f.offset)
	f.offset += int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		f.noteError(err)
	}
	return n, err
}

func (f *File) ReadAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	if closed {
		return 0, os.ErrClosed
	}
	n, err := f.entry.f.ReadAt(p, off)
	if err != nil && !errors.Is(err, io.EOF) {
		f.noteError(err)
	}
	return n, err
}

func (f *File) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	if !f.writable {
		return 0, os.ErrPermission
	}
	n, err := f.entry.f.WriteAt(p, f.offset)
	f.offset += int64(n)
	f.fs.attrs.invalidateAttr(f.remote)
	if err != nil {
		f.noteError(err)
	}
	return n, err
}

func (f *File) WriteAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	closed, writable := f.closed, f.writable
	f.mu.Unlock()
	if closed {
		return 0, os.ErrClosed
	}
	if !writable {
		return 0, os.ErrPermission
	}
	n, err := f.entry.f.WriteAt(p, off)
	f.fs.attrs.invalidateAttr(f.remote)
	if err != nil {
		f.noteError(err)
	}
	return n, err
}

func (f *File) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	switch whence {
	case io.SeekStart:
		f.offset = offset
	case io.SeekCurrent:
		f.offset += offset
	case io.SeekEnd:
		st, err := f.entry.f.Stat()
		if err != nil {
			f.noteError(err)
			return 0, err
		}
		f.offset = st.Size() + offset
	default:
		return 0, fmt.Errorf("invalid whence %d", whence)
	}
	if f.offset < 0 {
		f.offset = 0
		return 0, fmt.Errorf("negative offset")
	}
	return f.offset, nil
}

func (f *File) Truncate(size int64) error {
	f.mu.Lock()
	closed, writable := f.closed, f.writable
	f.mu.Unlock()
	if closed {
		return os.ErrClosed
	}
	if !writable {
		return os.ErrPermission
	}
	err := f.entry.f.Truncate(size)
	f.fs.attrs.invalidateAttr(f.remote)
	if err != nil {
		f.noteError(err)
	}
	return err
}

// Lock and Unlock are no-ops: NFSv3 locking is handled by the client with the
// locallocks mount option, and SFTP has no portable locking primitive.
func (f *File) Lock() error   { return nil }
func (f *File) Unlock() error { return nil }

func (f *File) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	f.mu.Unlock()
	return f.fs.files.release(f.entry)
}

// noteError retires the shared handle when the failure was the session dying.
func (f *File) noteError(err error) {
	if isConnError(err) {
		f.fs.conn.Invalidate(f.entry.gen)
		f.fs.files.dropAll()
		f.fs.attrs.clear()
	}
}
