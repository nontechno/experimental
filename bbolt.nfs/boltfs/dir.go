package boltfs

import (
	"os"
	"syscall"

	bolt "go.etcd.io/bbolt"
)

// dir is a directory container: either a bbolt bucket or the database root.
// The root supports nested buckets but not keys, so key operations on it fail
// with EPERM instead of panicking or corrupting anything.
type dir struct {
	tx *bolt.Tx
	b  *bolt.Bucket // nil means the database root
}

func (d dir) isRoot() bool { return d.b == nil }

func (d dir) bucket(name string) *bolt.Bucket {
	if d.b == nil {
		return d.tx.Bucket([]byte(name))
	}
	return d.b.Bucket([]byte(name))
}

func (d dir) cursor() *bolt.Cursor {
	if d.b == nil {
		return d.tx.Cursor()
	}
	return d.b.Cursor()
}

func (d dir) createBucket(name string) (*bolt.Bucket, error) {
	if d.b == nil {
		return d.tx.CreateBucket([]byte(name))
	}
	return d.b.CreateBucket([]byte(name))
}

func (d dir) deleteBucket(name string) error {
	if d.b == nil {
		return d.tx.DeleteBucket([]byte(name))
	}
	return d.b.DeleteBucket([]byte(name))
}

// put stores a file's content. errRootKey guards the database root, which
// cannot hold keys.
func (d dir) put(name string, value []byte) error {
	if d.b == nil {
		return errRootKey
	}
	return d.b.Put([]byte(name), value)
}

func (d dir) delete(name string) error {
	if d.b == nil {
		return errRootKey
	}
	return d.b.Delete([]byte(name))
}

// errRootKey is returned when a file would have to live at the database root.
var errRootKey = &rootKeyError{}

type rootKeyError struct{}

func (*rootKeyError) Error() string {
	return "bbolt stores no keys at the database root: run with -bucket NAME to root the filesystem inside a bucket"
}

// Unwrap lets callers (and go-nfs) treat this as a permission failure.
func (*rootKeyError) Unwrap() error { return os.ErrPermission }

type kind int

const (
	kindNone kind = iota
	kindFile
	kindDir
)

// lookup classifies name within d.
//
// A nil cursor value normally means "nested bucket", but an empty file value
// is indistinguishable from it, so the bucket is checked explicitly first.
// The returned value aliases the mmap and is only valid inside the
// transaction.
func lookup(d dir, name string) (kind, []byte) {
	if d.bucket(name) != nil {
		return kindDir, nil
	}
	if d.isRoot() {
		return kindNone, nil // the root holds nothing but buckets
	}
	k, v := d.cursor().Seek([]byte(name))
	if k != nil && string(k) == name {
		if v == nil {
			v = []byte{} // empty file, not a bucket: the bucket check ruled that out
		}
		return kindFile, v
	}
	return kindNone, nil
}

// walk descends through elems, all of which must be directories, and returns
// the container they name.
func walk(tx *bolt.Tx, elems []string) (dir, error) {
	d := dir{tx: tx}
	for i, name := range elems {
		k, _ := lookup(d, name)
		switch k {
		case kindDir:
			d = dir{tx: tx, b: d.bucket(name)}
		case kindFile:
			return dir{}, &os.PathError{Op: "walk", Path: joinPath(elems[:i+1]), Err: syscall.ENOTDIR}
		default:
			return dir{}, &os.PathError{Op: "walk", Path: joinPath(elems[:i+1]), Err: os.ErrNotExist}
		}
	}
	return d, nil
}

// parentOf resolves the container holding the last element of elems and
// returns it together with that element's name.
func parentOf(tx *bolt.Tx, elems []string) (dir, string, error) {
	if len(elems) == 0 {
		return dir{}, "", &os.PathError{Op: "resolve", Path: "/", Err: syscall.EBUSY}
	}
	d, err := walk(tx, elems[:len(elems)-1])
	if err != nil {
		return dir{}, "", err
	}
	return d, elems[len(elems)-1], nil
}

// copyBytes detaches a value from the mmap so it stays valid after the
// transaction ends. bbolt values must never escape their transaction.
func copyBytes(v []byte) []byte {
	if v == nil {
		return nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out
}
