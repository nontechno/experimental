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
	"sync"
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

	statvfs *statvfsCache
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
		files:    newFileCache(cfg.FileCacheMax, cfg.FileIdle.D(), conn.bounded),
		readOnly: cfg.ReadOnly,
		uid:      cfg.UID,
		gid:      cfg.GID,
		statvfs:  &statvfsCache{},
	}
	conn.SetOnReconnect(func() {
		f.files.dropAll()
		f.attrs.clear()
	})
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
// to escape the export root. go-nfs always joins with "/"; a backslash is an
// ordinary character in a POSIX file name and must stay one, or "a\b" would
// silently alias "a/b".
func (f *FS) resolve(name string) string {
	clean := path.Clean("/" + name)
	if clean == "/" {
		return f.root
	}
	if f.root == "/" {
		return clean
	}
	return f.root + clean
}

// do runs an SFTP operation, reconnecting and retrying once if the session
// turned out to be dead. name describes the operation for the log. Each
// attempt is bounded by the op timeout; a timed-out operation is not retried,
// since the path that stalled it would most likely stall the retry as well.
func (f *FS) do(name string, op func(c *sftp.Client) error) error {
	return f.doRetry(name, op, nil)
}

// doRetry is do for operations that are not idempotent. When the session
// dies after the server carried out the request but before its reply
// arrived, the retry fails ("no such file" for a remove that already
// happened, "file exists" for a symlink already made). reconcile then gets
// the retry's error and decides, by looking at the result, whether the
// operation had in fact succeeded. It runs only after a retry.
func (f *FS) doRetry(name string, op func(c *sftp.Client) error, reconcile func(c *sftp.Client, err error) error) error {
	call := func(c *sftp.Client, gen uint64) error {
		return f.conn.bounded(gen, name, func() error { return op(c) })
	}
	c, gen, err := f.conn.Client()
	if err != nil {
		return err
	}
	err = call(c, gen)
	if err == nil || !isConnError(err) {
		return err
	}
	f.conn.Invalidate(gen)
	f.files.dropAll()
	f.attrs.clear()

	c2, gen2, err2 := f.conn.Client()
	if err2 != nil {
		return err
	}
	err = call(c2, gen2)
	var te *opTimeoutError
	if err == nil || reconcile == nil || isConnError(err) || errors.As(err, &te) {
		return err
	}
	orig := err
	if rerr := f.conn.bounded(gen2, name+" (checking whether the first attempt went through)", func() error {
		return reconcile(c2, orig)
	}); rerr == nil {
		f.log.Debugf("%s: the first attempt had succeeded before the connection dropped", name)
		return nil
	}
	return orig
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
	err := f.do("stat "+remote, func(c *sftp.Client) error {
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
	err := f.do("lstat "+remote, func(c *sftp.Client) error {
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
	err := f.do("readdir "+remote, func(c *sftp.Client) error {
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
	err := f.do("readlink "+remote, func(c *sftp.Client) error {
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

	var gen uint64
	acquire := func() (*openFile, error) {
		cl, g, err := f.conn.Client()
		if err != nil {
			return nil, err
		}
		gen = g
		return f.files.acquire(cl, g, remote, flag, true)
	}
	entry, err := acquire()
	if err != nil && isConnError(err) {
		// Retire the dead session first, or the retry is handed the same
		// one whenever the keepalive has not noticed yet.
		f.conn.Invalidate(gen)
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
		if err := f.do("chmod "+remote, func(c *sftp.Client) error { return c.Chmod(remote, perm) }); err != nil {
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
	err := f.doRetry("rename "+from+" to "+to, func(c *sftp.Client) error {
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
	}, func(c *sftp.Client, err error) error {
		// The source is gone and the destination exists: the rename the
		// lost reply belonged to went through.
		if _, e := c.Lstat(from); !errors.Is(e, os.ErrNotExist) {
			return err
		}
		if _, e := c.Lstat(to); e != nil {
			return err
		}
		return nil
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
	err := f.doRetry("remove "+remote, func(c *sftp.Client) error { return c.Remove(remote) },
		func(_ *sftp.Client, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil // removed by the first attempt
			}
			return err
		})
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
	err := f.do("mkdir "+remote, func(c *sftp.Client) error { return c.MkdirAll(remote) })
	f.attrs.invalidate(remote)
	if err != nil {
		return err
	}
	if perm != 0 {
		if err := f.do("chmod "+remote, func(c *sftp.Client) error { return c.Chmod(remote, perm) }); err != nil {
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
	err := f.doRetry("symlink "+remote, func(c *sftp.Client) error { return c.Symlink(target, remote) },
		func(c *sftp.Client, err error) error {
			if got, e := c.ReadLink(remote); e == nil && got == target {
				return nil // made by the first attempt
			}
			return err
		})
	f.attrs.invalidate(remote)
	return err
}

// ----- billy.Change -----

func (f *FS) Chmod(name string, mode os.FileMode) error {
	if f.readOnly {
		return f.readOnlyErr()
	}
	remote := f.resolve(name)
	err := f.do("chmod "+remote, func(c *sftp.Client) error { return c.Chmod(remote, mode) })
	f.attrs.invalidate(remote)
	return err
}

func (f *FS) Chown(name string, uid, gid int) error {
	if f.readOnly {
		return f.readOnlyErr()
	}
	remote := f.resolve(name)
	err := f.do("chown "+remote, func(c *sftp.Client) error { return c.Chown(remote, uid, gid) })
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
	err := f.do("chtimes "+remote, func(c *sftp.Client) error { return c.Chtimes(remote, atime, mtime) })
	f.attrs.invalidate(remote)
	return err
}

// statvfsTTL is how long a free-space answer is reused. macOS asks for it
// constantly (Finder, every save dialog), and it does not change by the
// second in any way that matters.
const statvfsTTL = 5 * time.Second

// statvfsCache remembers the last FSSTAT answer, and that the server lacks
// the extension, so neither costs a round trip on every call.
type statvfsCache struct {
	mu          sync.Mutex
	st          *sftp.StatVFS
	err         error // a recent failure, reused like a result
	expires     time.Time
	unsupported bool
}

var errNoStatVFS = errors.New("server does not support statvfs@openssh.com")

// StatVFS reports free space for the NFS FSSTAT call. Not every server
// implements the OpenSSH extension, so callers must tolerate an error.
func (f *FS) StatVFS() (*sftp.StatVFS, error) {
	sc := f.statvfs
	sc.mu.Lock()
	if sc.unsupported {
		sc.mu.Unlock()
		return nil, errNoStatVFS
	}
	if (sc.st != nil || sc.err != nil) && time.Now().Before(sc.expires) {
		st, err := sc.st, sc.err
		sc.mu.Unlock()
		return st, err
	}
	sc.mu.Unlock()

	var st *sftp.StatVFS
	err := f.do("statvfs "+f.root, func(c *sftp.Client) error {
		// Servers without the extension do not all say "unsupported";
		// some fail generically. What they advertise is reliable.
		if _, ok := c.HasExtension("statvfs@openssh.com"); !ok {
			return errNoStatVFS
		}
		var e error
		st, e = c.StatVFS(f.root)
		return e
	})
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if err != nil {
		if errors.Is(err, errNoStatVFS) || isUnsupported(err) {
			sc.unsupported = true
			f.log.Debugf("statvfs unsupported by the server; reporting a fixed size")
		}
		// Some servers advertise the extension and fail it anyway; do not
		// ask again on every FSSTAT either way.
		sc.st, sc.err, sc.expires = nil, err, time.Now().Add(statvfsTTL)
		return nil, err
	}
	sc.st, sc.err, sc.expires = st, nil, time.Now().Add(statvfsTTL)
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
