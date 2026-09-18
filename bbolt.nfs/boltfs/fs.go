// Package boltfs exposes a bbolt database as a go-billy filesystem: buckets
// are directories, keys are file names and values are file contents.
//
// Every operation runs inside a single bbolt transaction, so each filesystem
// call is atomic and durable, and readers never observe a half-applied
// change. bbolt allows one writer at a time; writes are therefore serialized
// by the database itself rather than by locks here.
package boltfs

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
	bolt "go.etcd.io/bbolt"
)

// Options configures an FS. Zero values are replaced with defaults in New.
type Options struct {
	// RootBucket, if set, roots the filesystem inside this top-level bucket.
	// It is created on demand unless ReadOnly is set. Leave empty to expose
	// the database root, in which case the top level can hold directories
	// only, because bbolt stores no keys at the root.
	RootBucket string

	FileMode os.FileMode // default 0644
	DirMode  os.FileMode // default 0755
	UID, GID uint32      // owner reported for every entry

	// MaxFileSize caps a single value. Each write rewrites the whole value,
	// so large files are expensive; the default is 8 MiB.
	MaxFileSize int64

	// MaxRenameEntries and MaxRenameBytes bound a directory rename, which
	// copies the subtree inside one transaction. Defaults: 100k entries,
	// 256 MiB.
	MaxRenameEntries int
	MaxRenameBytes   int64

	// ReadOnly rejects every mutating call. Open the database read-only too
	// for a hard guarantee.
	ReadOnly bool

	// Deny refuses creation of entries whose base name it matches, e.g.
	// macOS .DS_Store and ._ AppleDouble files.
	Deny func(name string) bool

	// MTimeCacheSize bounds the modification-time table. Default 1<<16.
	MTimeCacheSize int
}

// FS is a billy.Filesystem backed by a bbolt database. It is safe for
// concurrent use. Chroot returns views that share the database, options and
// timestamp table.
type FS struct {
	db   *bolt.DB
	root []string // bucket chain from the database root to this view
	base []string // path of this view relative to the top-level view

	opts  *Options
	times *mtimes
}

var (
	_ billy.Filesystem = (*FS)(nil)
	_ billy.Change     = (*FS)(nil)
	_ billy.Capable    = (*FS)(nil)
)

// New returns a filesystem over db.
func New(db *bolt.DB, opts Options) (*FS, error) {
	o := opts
	if o.FileMode == 0 {
		o.FileMode = 0o644
	}
	if o.DirMode == 0 {
		o.DirMode = 0o755
	}
	if o.MaxFileSize <= 0 {
		o.MaxFileSize = 8 << 20
	}
	if o.MaxRenameEntries <= 0 {
		o.MaxRenameEntries = 100_000
	}
	if o.MaxRenameBytes <= 0 {
		o.MaxRenameBytes = 256 << 20
	}
	if o.MTimeCacheSize <= 0 {
		o.MTimeCacheSize = 1 << 16
	}
	if o.RootBucket != "" && !validName(o.RootBucket) {
		return nil, fmt.Errorf("invalid root bucket name %q", o.RootBucket)
	}

	f := &FS{db: db, opts: &o, times: newMtimes(o.MTimeCacheSize)}
	if o.RootBucket != "" {
		f.root = []string{o.RootBucket}
		if o.ReadOnly || db.IsReadOnly() {
			if err := db.View(func(tx *bolt.Tx) error {
				if tx.Bucket([]byte(o.RootBucket)) == nil {
					return fmt.Errorf("root bucket %q does not exist", o.RootBucket)
				}
				return nil
			}); err != nil {
				return nil, err
			}
		} else if err := db.Update(func(tx *bolt.Tx) error {
			_, err := tx.CreateBucketIfNotExists([]byte(o.RootBucket))
			return err
		}); err != nil {
			return nil, fmt.Errorf("create root bucket %q: %w", o.RootBucket, err)
		}
	}
	return f, nil
}

func pathErr(op, p string, err error) error {
	return &os.PathError{Op: op, Path: p, Err: err}
}

func (f *FS) writable(op, name string) error {
	if f.opts.ReadOnly {
		return pathErr(op, name, syscall.EROFS)
	}
	return nil
}

// checkName validates a new entry's base name.
func (f *FS) checkName(op, full, name string) error {
	if !validName(name) {
		return pathErr(op, full, syscall.EINVAL)
	}
	if f.opts.Deny != nil && f.opts.Deny(name) {
		return pathErr(op, full, os.ErrPermission)
	}
	return nil
}

// ---------------------------------------------------------------------------
// billy.Basic

func (f *FS) Create(filename string) (billy.File, error) {
	return f.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
}

func (f *FS) Open(filename string) (billy.File, error) {
	return f.OpenFile(filename, os.O_RDONLY, 0)
}

func (f *FS) OpenFile(filename string, flag int, _ os.FileMode) (billy.File, error) {
	elems := splitPath(filename)
	if len(elems) == 0 {
		return nil, pathErr("open", filename, syscall.EISDIR)
	}
	mutating := flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0
	if mutating {
		if err := f.writable("open", filename); err != nil {
			return nil, err
		}
	}

	var data []byte

	// A plain read needs no write transaction.
	if flag&(os.O_CREATE|os.O_TRUNC) == 0 {
		err := f.db.View(func(tx *bolt.Tx) error {
			d, name, err := parentOf(tx, f.chain(elems))
			if err != nil {
				return err
			}
			switch k, v := lookup(d, name); k {
			case kindDir:
				return pathErr("open", filename, syscall.EISDIR)
			case kindNone:
				return pathErr("open", filename, os.ErrNotExist)
			default:
				data = copyBytes(v)
				return nil
			}
		})
		if err != nil {
			return nil, wrapErr("open", filename, err)
		}
		return f.newFile(filename, elems, flag, data), nil
	}

	err := f.db.Update(func(tx *bolt.Tx) error {
		d, name, err := parentOf(tx, f.chain(elems))
		if err != nil {
			return err
		}
		k, v := lookup(d, name)
		switch {
		case k == kindDir:
			return pathErr("open", filename, syscall.EISDIR)
		case k == kindNone && flag&os.O_CREATE == 0:
			return pathErr("open", filename, os.ErrNotExist)
		case k == kindFile && flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0:
			return pathErr("open", filename, os.ErrExist)
		case k == kindNone:
			if err := f.checkName("open", filename, name); err != nil {
				return err
			}
			if err := d.put(name, nil); err != nil {
				return err
			}
			f.touch(elems)
			return nil
		case flag&os.O_TRUNC != 0 && len(v) > 0:
			if err := d.put(name, nil); err != nil {
				return err
			}
			f.touch(elems)
			return nil
		default:
			data = copyBytes(v)
			return nil
		}
	})
	if err != nil {
		return nil, wrapErr("open", filename, err)
	}
	return f.newFile(filename, elems, flag, data), nil
}

func (f *FS) Stat(filename string) (os.FileInfo, error) {
	elems := splitPath(filename)
	var info os.FileInfo
	err := f.db.View(func(tx *bolt.Tx) error {
		if len(elems) == 0 {
			if _, err := walk(tx, f.root); err != nil {
				return err
			}
			info = f.newDirInfo("/", f.absPath(nil))
			return nil
		}
		d, name, err := parentOf(tx, f.chain(elems))
		if err != nil {
			return err
		}
		switch k, v := lookup(d, name); k {
		case kindDir:
			info = f.newDirInfo(name, f.absPath(elems))
		case kindFile:
			info = f.newFileInfo(name, f.absPath(elems), int64(len(v)))
		default:
			return pathErr("stat", filename, os.ErrNotExist)
		}
		return nil
	})
	if err != nil {
		return nil, wrapErr("stat", filename, err)
	}
	return info, nil
}

func (f *FS) Lstat(filename string) (os.FileInfo, error) { return f.Stat(filename) }

func (f *FS) Rename(oldpath, newpath string) error {
	if err := f.writable("rename", oldpath); err != nil {
		return err
	}
	from, to := splitPath(oldpath), splitPath(newpath)
	switch {
	case len(from) == 0 || len(to) == 0:
		return pathErr("rename", oldpath, syscall.EBUSY)
	case joinPath(from) == joinPath(to):
		return nil
	case strings.HasPrefix(joinPath(to), joinPath(from)+"/"):
		// Moving a directory inside itself.
		return pathErr("rename", oldpath, syscall.EINVAL)
	}
	if err := f.checkName("rename", newpath, to[len(to)-1]); err != nil {
		return err
	}

	err := f.db.Update(func(tx *bolt.Tx) error {
		srcDir, srcName, err := parentOf(tx, f.chain(from))
		if err != nil {
			return err
		}
		srcKind, srcVal := lookup(srcDir, srcName)
		if srcKind == kindNone {
			return pathErr("rename", oldpath, os.ErrNotExist)
		}
		dstDir, dstName, err := parentOf(tx, f.chain(to))
		if err != nil {
			return err
		}
		dstKind, _ := lookup(dstDir, dstName)

		switch {
		case dstKind == kindDir && srcKind == kindFile:
			return pathErr("rename", newpath, syscall.EISDIR)
		case dstKind == kindFile && srcKind == kindDir:
			return pathErr("rename", newpath, syscall.ENOTDIR)
		case dstKind == kindDir:
			b := dstDir.bucket(dstName)
			if k, _ := b.Cursor().First(); k != nil {
				return pathErr("rename", newpath, syscall.ENOTEMPTY)
			}
			if err := dstDir.deleteBucket(dstName); err != nil {
				return err
			}
		}

		if srcKind == kindFile {
			if err := dstDir.put(dstName, srcVal); err != nil {
				return err
			}
			if err := srcDir.delete(srcName); err != nil {
				return err
			}
		} else {
			dst, err := dstDir.createBucket(dstName)
			if err != nil {
				return err
			}
			budget := renameBudget{
				entries: f.opts.MaxRenameEntries, bytes: f.opts.MaxRenameBytes,
				startEntries: f.opts.MaxRenameEntries, startBytes: f.opts.MaxRenameBytes,
			}
			if err := copyBucket(srcDir.bucket(srcName), dst, &budget); err != nil {
				return err
			}
			if err := srcDir.deleteBucket(srcName); err != nil {
				return err
			}
		}
		f.times.forget(f.absPath(from))
		f.touch(from, to)
		return nil
	})
	return wrapErr("rename", oldpath, err)
}

type renameBudget struct {
	entries      int
	bytes        int64
	startEntries int
	startBytes   int64
}

// tooLargeError reports EFBIG with enough detail to act on: the raw errno
// reaches the client as "file too large", which says nothing about which
// limit was hit or how to raise it.
type tooLargeError struct {
	what        string // "file" or "directory"
	unit        string // defaults to "bytes"
	size, limit int64
	flag        string
}

func (e *tooLargeError) Error() string {
	unit := e.unit
	if unit == "" {
		unit = "bytes"
	}
	return fmt.Sprintf("%s needs %d %s, over the limit of %d; raise %s to allow it",
		e.what, e.size, unit, e.limit, e.flag)
}

// Unwrap lets go-nfs map this to NFS3ERR_FBIG.
func (e *tooLargeError) Unwrap() error { return syscall.EFBIG }

// copyBucket copies a bucket subtree. The whole rename runs in one
// transaction, so it either completes or leaves the database untouched.
func copyBucket(src, dst *bolt.Bucket, budget *renameBudget) error {
	c := src.Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		budget.entries--
		if budget.entries < 0 {
			return &tooLargeError{what: "directory", unit: "entries",
				size: int64(budget.startEntries) + 1, limit: int64(budget.startEntries),
				flag: "-max-rename-entries"}
		}
		if sub := src.Bucket(k); sub != nil {
			nested, err := dst.CreateBucket(k)
			if err != nil {
				return err
			}
			if err := copyBucket(sub, nested, budget); err != nil {
				return err
			}
			continue
		}
		budget.bytes -= int64(len(v))
		if budget.bytes < 0 {
			return &tooLargeError{what: "directory", size: budget.startBytes - budget.bytes,
				limit: budget.startBytes, flag: "-max-rename-bytes"}
		}
		if err := dst.Put(k, v); err != nil {
			return err
		}
	}
	return nil
}

func (f *FS) Remove(filename string) error {
	if err := f.writable("remove", filename); err != nil {
		return err
	}
	elems := splitPath(filename)
	if len(elems) == 0 {
		return pathErr("remove", filename, syscall.EBUSY)
	}
	err := f.db.Update(func(tx *bolt.Tx) error {
		d, name, err := parentOf(tx, f.chain(elems))
		if err != nil {
			return err
		}
		switch k, _ := lookup(d, name); k {
		case kindNone:
			return pathErr("remove", filename, os.ErrNotExist)
		case kindDir:
			b := d.bucket(name)
			if k, _ := b.Cursor().First(); k != nil {
				return pathErr("remove", filename, syscall.ENOTEMPTY)
			}
			if err := d.deleteBucket(name); err != nil {
				return err
			}
		default:
			if err := d.delete(name); err != nil {
				return err
			}
		}
		f.times.forget(f.absPath(elems))
		f.touch(elems)
		return nil
	})
	return wrapErr("remove", filename, err)
}

func (f *FS) Join(elem ...string) string { return path.Join(elem...) }

// ---------------------------------------------------------------------------
// billy.TempFile

func (f *FS) TempFile(dir, prefix string) (billy.File, error) {
	var b [8]byte
	for i := 0; i < 10; i++ {
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		name := path.Join(dir, prefix+hex.EncodeToString(b[:]))
		fh, err := f.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, os.ErrExist) {
			return fh, err
		}
	}
	return nil, pathErr("tempfile", dir, os.ErrExist)
}

// ---------------------------------------------------------------------------
// billy.Dir

// ReadDir lists a directory. Names that cannot be represented as filenames
// (empty, containing "/" or NUL, over 255 bytes) are skipped rather than
// mangled; Stat on such a key would not round-trip.
func (f *FS) ReadDir(dirname string) ([]os.FileInfo, error) {
	elems := splitPath(dirname)
	var infos []os.FileInfo
	err := f.db.View(func(tx *bolt.Tx) error {
		d, err := walk(tx, f.chain(elems))
		if err != nil {
			return err
		}
		type ent struct {
			name  string
			isDir bool
			size  int64
		}
		var ents []ent
		c := d.cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			name := string(k)
			if !validName(name) {
				continue
			}
			// A nil value is a nested bucket or an empty file; ask the
			// bucket index which it is.
			isDir := v == nil && d.bucket(name) != nil
			ents = append(ents, ent{name: name, isDir: isDir, size: int64(len(v))})
		}
		sort.Slice(ents, func(i, j int) bool { return ents[i].name < ents[j].name })
		infos = make([]os.FileInfo, 0, len(ents))
		for _, e := range ents {
			abs := f.absPath(append(append([]string{}, elems...), e.name))
			if e.isDir {
				infos = append(infos, f.newDirInfo(e.name, abs))
			} else {
				infos = append(infos, f.newFileInfo(e.name, abs, e.size))
			}
		}
		return nil
	})
	if err != nil {
		return nil, wrapErr("readdir", dirname, err)
	}
	return infos, nil
}

func (f *FS) MkdirAll(filename string, _ os.FileMode) error {
	if err := f.writable("mkdir", filename); err != nil {
		return err
	}
	elems := splitPath(filename)
	if len(elems) == 0 {
		return nil
	}
	for _, name := range elems {
		if err := f.checkName("mkdir", filename, name); err != nil {
			return err
		}
	}
	err := f.db.Update(func(tx *bolt.Tx) error {
		d := dir{tx: tx}
		chain := f.chain(elems)
		for i, name := range chain {
			switch k, _ := lookup(d, name); k {
			case kindDir:
				d = dir{tx: tx, b: d.bucket(name)}
			case kindFile:
				return pathErr("mkdir", joinPath(chain[:i+1]), syscall.ENOTDIR)
			default:
				b, err := d.createBucket(name)
				if err != nil {
					return err
				}
				d = dir{tx: tx, b: b}
				if i >= len(f.root) {
					f.touch(elems[:i+1-len(f.root)])
				}
			}
		}
		return nil
	})
	return wrapErr("mkdir", filename, err)
}

// ---------------------------------------------------------------------------
// billy.Symlink — bbolt has no link type, so symlinks are not supported.

func (f *FS) Symlink(_, link string) error {
	return pathErr("symlink", link, syscall.ENOTSUP)
}

func (f *FS) Readlink(link string) (string, error) {
	return "", pathErr("readlink", link, syscall.EINVAL)
}

// ---------------------------------------------------------------------------
// billy.Chroot

func (f *FS) Chroot(p string) (billy.Filesystem, error) {
	elems := splitPath(p)
	if len(elems) == 0 {
		return f, nil
	}
	return &FS{
		db:    f.db,
		root:  f.chain(elems),
		base:  append(append([]string{}, f.base...), elems...),
		opts:  f.opts,
		times: f.times,
	}, nil
}

// Root reports the bucket path this view is rooted at, from the database root.
func (f *FS) Root() string { return "/" + joinPath(f.root) }

// Base reports this view's path relative to the top-level view.
func (f *FS) Base() []string { return f.base }

// DB exposes the underlying database (used for FSSTAT).
func (f *FS) DB() *bolt.DB { return f.db }

// ---------------------------------------------------------------------------
// billy.Change — bbolt has nowhere to keep modes, owners or times. The calls
// succeed so that cp, touch and editors do not fail, but nothing is stored.

func (f *FS) Chmod(name string, _ os.FileMode) error { return f.writable("chmod", name) }
func (f *FS) Lchown(name string, _, _ int) error     { return f.writable("lchown", name) }
func (f *FS) Chown(name string, _, _ int) error      { return f.writable("chown", name) }

func (f *FS) Chtimes(name string, _, _ time.Time) error {
	return f.writable("chtimes", name)
}

// Capabilities implements billy.Capable.
func (f *FS) Capabilities() billy.Capability {
	if f.opts.ReadOnly {
		return billy.ReadCapability | billy.SeekCapability
	}
	return billy.WriteCapability | billy.ReadCapability | billy.ReadAndWriteCapability |
		billy.SeekCapability | billy.TruncateCapability
}

// ---------------------------------------------------------------------------
// helpers

// touch records new modification times for the given paths and their parents.
func (f *FS) touch(paths ...[]string) {
	seen := make([]string, 0, 2*len(paths))
	for _, p := range paths {
		seen = append(seen, f.absPath(p))
		if len(p) > 0 {
			seen = append(seen, f.absPath(p[:len(p)-1]))
		}
	}
	f.times.touch(seen...)
}

// wrapErr keeps *os.PathError values from deeper layers intact and wraps bare
// bbolt errors, mapping them onto errno values go-nfs understands.
func wrapErr(op, name string, err error) error {
	if err == nil {
		return nil
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		return err
	}
	switch {
	case errors.Is(err, bolt.ErrBucketExists):
		return pathErr(op, name, os.ErrExist)
	case errors.Is(err, bolt.ErrIncompatibleValue):
		return pathErr(op, name, syscall.ENOTDIR)
	case errors.Is(err, bolt.ErrKeyTooLarge), errors.Is(err, bolt.ErrValueTooLarge):
		return pathErr(op, name, syscall.EFBIG)
	case errors.Is(err, bolt.ErrTxNotWritable), errors.Is(err, bolt.ErrDatabaseReadOnly):
		return pathErr(op, name, syscall.EROFS)
	case errors.Is(err, os.ErrPermission):
		return pathErr(op, name, err)
	}
	return pathErr(op, name, err)
}
