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
	"sync/atomic"
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

	// noPosixRename warns, once, that renames are not atomic.
	noPosixRename *sync.Once
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

		noPosixRename: &sync.Once{},
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
	f.retire(gen)

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

// retire drops the session gen together with everything tied to it, the
// pooled handles and the cached attributes, so the next request starts over
// on a fresh session.
func (f *FS) retire(gen uint64) {
	f.conn.Invalidate(gen)
	f.files.dropAll()
	f.attrs.clear()
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
	if flag&os.O_CREATE == 0 {
		// go-nfs truncates (SETATTR size) through O_WRONLY|O_EXCL. Without
		// O_CREATE, O_EXCL means nothing in POSIX, but sftp passes it on as
		// SSH_FXF_EXCL, and a server that reads it as "fail if the file
		// exists" would refuse every truncate. It also kept the handle out
		// of the pool for no reason.
		flag &^= os.O_EXCL
	}
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
		f.retire(gen)
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

// randomSuffix returns n random bytes as hex, for names that must not
// collide with anything already there.
func randomSuffix(n int) string {
	b := make([]byte, n)
	rand.Read(b) // never fails since Go 1.24
	return hex.EncodeToString(b)
}

func (f *FS) TempFile(dir, prefix string) (billy.File, error) {
	if f.readOnly {
		return nil, f.readOnlyErr()
	}
	for i := 0; i < 32; i++ {
		name := path.Join(dir, prefix+randomSuffix(8))
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
	// Renaming a directory moves everything under it, and a pooled handle
	// or cached stat for "dir/f" would otherwise keep being used for that
	// name although it now refers to the moved file. Walking the caches for
	// a subtree costs a scan, so it is skipped when the cache knows the
	// source is a regular file: the common save-to-temp-then-rename case.
	tree := !f.knownRegular(from)
	f.forget(tree, from, to)
	defer f.forget(tree, from, to)

	// parked is set while the fallback may have the destination moved aside.
	// Atomic, because after a timeout the attempt can still be running.
	var parked atomic.Bool
	aside := asideName(to)
	err := f.doRetry("rename "+from+" to "+to, func(c *sftp.Client) error {
		if parked.Load() {
			// The attempt that moved the destination aside lost its session.
			// Nothing is redone on top of that; settleAside sorts it out.
			return errRenameInterrupted
		}
		// PosixRename replaces the destination atomically. Only fall back if
		// the server does not implement the OpenSSH extension: any other
		// failure is a real error and must not cost the caller the
		// destination file.
		e := c.PosixRename(from, to)
		if e == nil || !isUnsupported(e) {
			return e
		}
		f.noPosixRename.Do(func() {
			f.log.Warnf("the server lacks posix-rename@openssh.com: a rename onto an existing name is done in steps, and is not atomic")
		})
		return f.renameReplacing(c, from, to, aside, &parked)
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
	if parked.Load() {
		// An attempt was cut off with the destination possibly moved aside.
		var te *opTimeoutError
		if errors.As(err, &te) {
			// Its session was dropped, and a new one may take a while.
			go f.settleAside(from, to, aside)
		} else {
			f.settleAside(from, to, aside)
		}
	}
	if errors.Is(err, errRenameInterrupted) {
		err = &os.PathError{Op: "rename", Path: from, Err: syscall.EIO}
	}
	return err
}

// asideName is where the fallback rename parks the destination meanwhile:
// a short random name in the same directory, so it cannot run past NAME_MAX
// however long the destination's name is.
func asideName(to string) string {
	return path.Join(path.Dir(to), ".sshnfs-"+randomSuffix(6))
}

var errRenameInterrupted = errors.New("rename interrupted by a lost connection")

// knownRegular reports whether the attribute cache knows p is a regular
// file. Anything else, a symlink to a directory included, might lead to a
// subtree.
func (f *FS) knownRegular(p string) bool {
	fi, ok := f.attrs.getAttr(p, true)
	return ok && fi.Mode().IsRegular()
}

// forget drops pooled handles and cached attributes for the paths, and with
// tree for everything under them too, in one pass over each cache.
func (f *FS) forget(tree bool, paths ...string) {
	if tree {
		f.files.dropTree(paths...)
		f.attrs.invalidateTree(paths...)
		return
	}
	for _, p := range paths {
		f.files.drop(p)
		f.attrs.invalidate(p)
	}
}

// lookupEntry describes the directory entry p itself, without following it.
// Its error matches os.ErrNotExist only when p is known to be absent. Lstat
// is the normal way, but some servers' lstat follows a dangling symlink and
// reports it missing. A "missing" answer is therefore checked with
// readlink, and only if the server cannot tell (it lacks readlink) against
// the parent's listing, which is expensive in a big directory.
func lookupEntry(c *sftp.Client, p string) (os.FileInfo, error) {
	fi, err := c.Lstat(p)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return fi, err
	}
	_, lerr := c.ReadLink(p)
	switch {
	case lerr == nil:
		return symlinkInfo{name: path.Base(p)}, nil
	case errors.Is(lerr, os.ErrNotExist):
		return nil, err
	case isConnError(lerr):
		return nil, lerr
	}
	list, lerr := c.ReadDir(path.Dir(p))
	if lerr != nil {
		return nil, lerr
	}
	name := path.Base(p)
	for _, e := range list {
		if e.Name() == name {
			return e, nil
		}
	}
	return nil, err
}

// symlinkInfo stands in for the attributes of a symlink that readlink found
// but lstat would not describe.
type symlinkInfo struct{ name string }

func (s symlinkInfo) Name() string       { return s.name }
func (s symlinkInfo) Size() int64        { return 0 }
func (s symlinkInfo) Mode() os.FileMode  { return os.ModeSymlink | 0o777 }
func (s symlinkInfo) ModTime() time.Time { return time.Time{} }
func (s symlinkInfo) IsDir() bool        { return false }
func (s symlinkInfo) Sys() interface{}   { return nil }

// renameReplacing is rename for servers without posix-rename@openssh.com.
// Plain SFTP rename refuses to replace an existing destination, while NFS
// RENAME must replace it. The destination is moved aside (to aside) rather
// than removed, so that whatever makes the real rename fail (a missing source, a
// source directory without write permission) cannot cost the caller the
// destination: on failure it is put back.
//
// parked is set from just before the destination is moved aside until it
// is known to be back or replaced. A connection error in between returns
// with parked still set, and Rename then calls settleAside.
//
// The steps are not atomic: another client can briefly see the name
// missing. Nor can SFTP tell that the two names are hard links to one file,
// which POSIX would treat as a no-op; here the source name is renamed and
// the destination's old link removed.
func (f *FS) renameReplacing(c *sftp.Client, from, to, aside string, parked *atomic.Bool) error {
	if from == to {
		// POSIX: renaming a name to itself changes nothing.
		_, err := c.Lstat(from)
		return err
	}
	first := c.Rename(from, to)
	if first == nil || isConnError(first) {
		return first
	}
	// keep returns err if it is the connection's, else first: the reason
	// the rename was refused.
	keep := func(err error) error {
		if isConnError(err) {
			return err
		}
		return first
	}
	src, err := lookupEntry(c, from)
	if err != nil {
		return keep(err) // nothing to move
	}
	dst, err := lookupEntry(c, to)
	if err != nil {
		return keep(err) // nothing in the way, so the failure is something else
	}
	// As in POSIX: a directory replaces only an empty directory, and a
	// non-directory only a non-directory.
	if src.IsDir() != dst.IsDir() {
		return first
	}
	if dst.IsDir() {
		entries, err := c.ReadDir(to)
		if err != nil {
			return keep(err)
		}
		if len(entries) > 0 {
			return first
		}
	}

	parked.Store(true)
	if err := c.Rename(to, aside); err != nil {
		if isConnError(err) {
			return err
		}
		parked.Store(false)
		return first
	}
	if err := c.Rename(from, to); err != nil {
		if isConnError(err) {
			return err
		}
		if rerr := c.Rename(aside, to); rerr != nil {
			if isConnError(rerr) {
				return rerr
			}
			f.log.Errorf("rename %s to %s failed (%v), and putting the original %s back failed too (%v): it is now %s",
				from, to, err, to, rerr, aside)
		}
		parked.Store(false)
		return err
	}
	// The rename is done, so aside is certainly the replaced destination.
	parked.Store(false)
	if dst.IsDir() {
		err = c.RemoveDirectory(aside)
	} else {
		err = c.Remove(aside)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		f.log.Warnf("renamed %s to %s, but the replaced %s could not be removed and is left as %s: %v",
			from, to, to, aside, err)
	}
	return nil
}

// settleAside runs on a fresh session after a replacing rename was cut off
// with the destination possibly moved aside. With the destination name
// empty the rename cannot have happened, so the original is put back; plain
// SFTP rename never replaces, so that cannot clobber a file another client
// made meanwhile. With the name taken it is not certain whose file that is,
// so the original is never deleted: it is left in place, and the log says
// where.
func (f *FS) settleAside(from, to, aside string) {
	c, gen, err := f.conn.Client()
	if err == nil {
		err = f.conn.bounded(gen, "settle rename "+from+" to "+to, func() error {
			if _, err := lookupEntry(c, aside); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil // never moved, or already back or removed
				}
				return err
			}
			_, err := lookupEntry(c, to)
			switch {
			case err == nil:
				f.log.Warnf("a rename of %s to %s was cut off; %s exists now, and the file it replaced is left as %s: delete it if it is not needed",
					from, to, to, aside)
				return nil
			case errors.Is(err, os.ErrNotExist):
				return c.Rename(aside, to)
			}
			return err
		})
		f.forget(false, to, aside)
	}
	if err != nil {
		f.log.Errorf("a rename of %s to %s was cut off, and checking on it failed (%v): the original %s may be left as %s, move it back by hand",
			from, to, err, to, aside)
	}
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
