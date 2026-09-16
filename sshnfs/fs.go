package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/pkg/sftp"
)

// FS exposes a directory on an SSH server as a billy filesystem, which go-nfs
// then serves as NFSv3.
type FS struct {
	conn  *Conn
	log   *Logger
	root  string // absolute path on the remote host
	attrs *attrCache
	files *fileCache

	readOnly bool
	uid, gid int
}

var (
	_ billy.Filesystem = (*FS)(nil)
	_ billy.Change     = (*FS)(nil)
	_ billy.Capable    = (*FS)(nil)
)

func NewFS(cfg *Config, conn *Conn, log *Logger, root string) *FS {
	f := &FS{
		conn:     conn,
		log:      log,
		root:     normalizeRoot(root),
		attrs:    newAttrCache(cfg.AttrCacheTTL.D(), cfg.DirCacheTTL.D()),
		files:    newFileCache(cfg.FileCacheMax, cfg.FileIdle.D()),
		readOnly: cfg.ReadOnly,
		uid:      cfg.UID,
		gid:      cfg.GID,
	}
	conn.onReconnect = func() {
		f.files.dropAll()
		f.attrs.clear()
	}
	return f
}

func (f *FS) Close() {
	f.files.Close()
}

func normalizeRoot(root string) string {
	if root == "" {
		return "/"
	}
	root = path.Clean(root)
	if root != "/" {
		root = strings.TrimSuffix(root, "/")
	}
	return root
}

// resolve maps a path inside the export to an absolute remote path, refusing
// to escape the export root.
func (f *FS) resolve(name string) string {
	clean := path.Clean("/" + strings.ReplaceAll(name, "\\", "/"))
	if clean == "/" {
		return f.root
	}
	if f.root == "/" {
		return clean
	}
	return f.root + clean
}

// do runs an SFTP operation, reconnecting and retrying once if the session
// turned out to be dead.
func (f *FS) do(op func(c *sftp.Client) error) error {
	c, gen, err := f.conn.Client()
	if err != nil {
		return err
	}
	err = op(c)
	if err == nil || !isConnError(err) {
		return err
	}
	f.conn.Invalidate(gen)
	f.files.dropAll()
	f.attrs.clear()

	c2, _, err2 := f.conn.Client()
	if err2 != nil {
		return err
	}
	return op(c2)
}

func (f *FS) Root() string { return f.root }

func (f *FS) Join(elem ...string) string { return path.Join(elem...) }

func (f *FS) Capabilities() billy.Capability {
	caps := billy.ReadCapability | billy.SeekCapability | billy.LockCapability
	if !f.readOnly {
		caps |= billy.WriteCapability | billy.ReadAndWriteCapability | billy.TruncateCapability
	}
	return caps
}

func (f *FS) Chroot(p string) (billy.Filesystem, error) {
	child := *f
	child.root = normalizeRoot(f.resolve(p))
	return &child, nil
}

// ----- metadata -----

func (f *FS) Stat(name string) (os.FileInfo, error) {
	remote := f.resolve(name)
	if fi, ok := f.attrs.getAttr(remote, false); ok {
		return fi, nil
	}
	var fi os.FileInfo
	err := f.do(func(c *sftp.Client) error {
		var e error
		fi, e = c.Stat(remote)
		return e
	})
	if err != nil {
		return nil, err
	}
	wrapped := f.wrap(remote, fi)
	f.attrs.putAttr(remote, false, wrapped)
	return wrapped, nil
}

func (f *FS) Lstat(name string) (os.FileInfo, error) {
	remote := f.resolve(name)
	if fi, ok := f.attrs.getAttr(remote, true); ok {
		return fi, nil
	}
	var fi os.FileInfo
	err := f.do(func(c *sftp.Client) error {
		var e error
		fi, e = c.Lstat(remote)
		return e
	})
	if err != nil {
		return nil, err
	}
	wrapped := f.wrap(remote, fi)
	f.attrs.putAttr(remote, true, wrapped)
	return wrapped, nil
}

func (f *FS) ReadDir(name string) ([]os.FileInfo, error) {
	remote := f.resolve(name)
	if list, ok := f.attrs.getDir(remote); ok {
		return list, nil
	}
	var raw []os.FileInfo
	err := f.do(func(c *sftp.Client) error {
		var e error
		raw, e = c.ReadDir(remote)
		return e
	})
	if err != nil {
		return nil, err
	}
	out := make([]os.FileInfo, 0, len(raw))
	for _, fi := range raw {
		child := path.Join(remote, fi.Name())
		// READDIR reports the entry itself, never the symlink target, so
		// these belong on the lstat side of the cache.
		wrapped := f.wrap(child, fi)
		f.attrs.putAttr(child, true, wrapped)
		out = append(out, wrapped)
	}
	// go-nfs relies on a stable ordering for READDIR cookies.
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	f.attrs.putDir(remote, out)
	return out, nil
}

func (f *FS) Readlink(name string) (string, error) {
	remote := f.resolve(name)
	var target string
	err := f.do(func(c *sftp.Client) error {
		var e error
		target, e = c.ReadLink(remote)
		return e
	})
	if err != nil {
		return "", err
	}
	// Targets are returned verbatim, exactly as a kernel NFS server does: the
	// client resolves them, and rewriting an absolute target would not make it
	// resolve any better on the other side.
	return target, nil
}

// exists answers "is there already a name here?" preferring a cached answer.
// go-nfs stats the path immediately before CREATE and MKDIR, so this is
// normally free; only a genuinely new name costs a round trip. A path we
// cannot stat for any reason other than absence is treated as existing, so we
// never touch its mode.
func (f *FS) exists(name string) bool {
	remote := f.resolve(name)
	if _, ok := f.attrs.getAttr(remote, true); ok {
		return true
	}
	if _, ok := f.attrs.getAttr(remote, false); ok {
		return true
	}
	_, err := f.Lstat(name)
	return !errors.Is(err, os.ErrNotExist)
}

// ----- mutation -----

func (f *FS) readOnlyErr() error {
	return billy.ErrReadOnly
}

func (f *FS) Create(name string) (billy.File, error) {
	return f.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
}

func (f *FS) Open(name string) (billy.File, error) {
	return f.OpenFile(name, os.O_RDONLY, 0)
}

func (f *FS) OpenFile(name string, flag int, perm os.FileMode) (billy.File, error) {
	writable := flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0
	if f.readOnly && writable {
		return nil, f.readOnlyErr()
	}
	remote := f.resolve(name)

	// Only a file we actually create should have its mode set below. NFS
	// CREATE is legal on an existing file (it just truncates), and clobbering
	// the mode of an existing file would briefly widen its permissions.
	isNew := flag&os.O_CREATE != 0 && !f.exists(name)

	acquire := func() (*openFile, error) {
		cl, gen, err := f.conn.Client()
		if err != nil {
			return nil, err
		}
		return f.files.acquire(cl, gen, remote, flag, true)
	}
	entry, err := acquire()
	if err != nil && isConnError(err) {
		f.files.dropAll()
		f.attrs.clear()
		entry, err = acquire()
	}
	if err != nil {
		return nil, err
	}

	if isNew && perm != 0 {
		// SFTP's open does not carry a mode, so apply it explicitly. A failure
		// here is not fatal: the file exists and is usable.
		if err := f.do(func(c *sftp.Client) error { return c.Chmod(remote, perm) }); err != nil {
			f.log.Debugf("chmod %s after create: %v", remote, err)
		}
	}
	switch {
	case flag&os.O_CREATE != 0:
		// A name may have appeared, so the containing directory's listing is
		// stale too. Done for any create attempt, not just the ones exists()
		// judged new, so an unstattable path cannot leave a stale listing.
		f.attrs.invalidate(remote)
	case flag&os.O_TRUNC != 0:
		f.attrs.invalidateAttr(remote)
	}

	return &File{
		fs:       f,
		name:     name,
		remote:   remote,
		entry:    entry,
		writable: entry.writable,
	}, nil
}

func (f *FS) TempFile(dir, prefix string) (billy.File, error) {
	if f.readOnly {
		return nil, f.readOnlyErr()
	}
	for i := 0; i < 32; i++ {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		name := path.Join(dir, prefix+hex.EncodeToString(b[:]))
		file, err := f.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("could not create a temporary file in %s", dir)
}

func (f *FS) Rename(oldpath, newpath string) error {
	if f.readOnly {
		return f.readOnlyErr()
	}
	from, to := f.resolve(oldpath), f.resolve(newpath)
	f.files.drop(from)
	f.files.drop(to)
	err := f.do(func(c *sftp.Client) error {
		// PosixRename replaces the destination atomically. Only fall back if
		// the server does not implement the OpenSSH extension: any other
		// failure is a real error and must not cost the caller the
		// destination file.
		e := c.PosixRename(from, to)
		if e == nil || !isUnsupported(e) {
			return e
		}
		if e := c.Rename(from, to); e == nil {
			return nil
		}
		// Plain SFTP rename refuses to clobber. NFS RENAME is defined to
		// replace, so retry once after removing the destination.
		if _, statErr := c.Stat(to); statErr != nil {
			return c.Rename(from, to)
		}
		if e := c.Remove(to); e != nil {
			return e
		}
		return c.Rename(from, to)
	})
	f.attrs.invalidate(from)
	f.attrs.invalidate(to)
	return err
}

func (f *FS) Remove(name string) error {
	if f.readOnly {
		return f.readOnlyErr()
	}
	remote := f.resolve(name)
	f.files.drop(remote)
	err := f.do(func(c *sftp.Client) error { return c.Remove(remote) })
	f.attrs.invalidate(remote)
	return err
}

func (f *FS) MkdirAll(name string, perm os.FileMode) error {
	if f.readOnly {
		return f.readOnlyErr()
	}
	remote := f.resolve(name)
	// billy's MkdirAll is a no-op on an existing directory; in particular it
	// must not change its mode.
	if f.exists(name) {
		fi, err := f.Lstat(name)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		return &os.PathError{Op: "mkdir", Path: name, Err: syscall.ENOTDIR}
	}
	err := f.do(func(c *sftp.Client) error { return c.MkdirAll(remote) })
	f.attrs.invalidate(remote)
	if err != nil {
		return err
	}
	if perm != 0 {
		if err := f.do(func(c *sftp.Client) error { return c.Chmod(remote, perm) }); err != nil {
			f.log.Debugf("chmod %s after mkdir: %v", remote, err)
		}
	}
	return nil
}

func (f *FS) Symlink(target, link string) error {
	if f.readOnly {
		return f.readOnlyErr()
	}
	remote := f.resolve(link)
	err := f.do(func(c *sftp.Client) error { return c.Symlink(target, remote) })
	f.attrs.invalidate(remote)
	return err
}

// ----- billy.Change -----

func (f *FS) Chmod(name string, mode os.FileMode) error {
	if f.readOnly {
		return f.readOnlyErr()
	}
	remote := f.resolve(name)
	err := f.do(func(c *sftp.Client) error { return c.Chmod(remote, mode) })
	f.attrs.invalidate(remote)
	return err
}

func (f *FS) Chown(name string, uid, gid int) error {
	if f.readOnly {
		return f.readOnlyErr()
	}
	remote := f.resolve(name)
	err := f.do(func(c *sftp.Client) error { return c.Chown(remote, uid, gid) })
	f.attrs.invalidate(remote)
	return err
}

// Lchown falls back to Chown: SFTP has no lchown request.
func (f *FS) Lchown(name string, uid, gid int) error {
	return f.Chown(name, uid, gid)
}

func (f *FS) Chtimes(name string, atime, mtime time.Time) error {
	if f.readOnly {
		return f.readOnlyErr()
	}
	remote := f.resolve(name)
	err := f.do(func(c *sftp.Client) error { return c.Chtimes(remote, atime, mtime) })
	f.attrs.invalidate(remote)
	return err
}

// StatVFS reports free space for the NFS FSSTAT call. Not every server
// implements the OpenSSH extension, so callers must tolerate an error.
func (f *FS) StatVFS() (*sftp.StatVFS, error) {
	var st *sftp.StatVFS
	err := f.do(func(c *sftp.Client) error {
		var e error
		st, e = c.StatVFS(f.root)
		return e
	})
	if err != nil {
		return nil, err
	}
	return st, nil
}

// isUnsupported reports whether the server answered "operation unsupported",
// which is the only case in which falling back to a non-atomic path is safe.
func isUnsupported(err error) bool {
	if errors.Is(err, sftp.ErrSSHFxOpUnsupported) {
		return true
	}
	var se *sftp.StatusError
	if errors.As(err, &se) {
		return se.FxCode() == sftp.ErrSSHFxOpUnsupported
	}
	return false
}
