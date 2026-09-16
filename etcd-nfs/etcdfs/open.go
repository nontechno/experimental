package etcdfs

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path"
	"syscall"

	"github.com/go-git/go-billy/v5"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func (f *FS) Create(filename string) (billy.File, error) {
	return f.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
}

func (f *FS) Open(filename string) (billy.File, error) {
	return f.OpenFile(filename, os.O_RDONLY, 0)
}

// OpenFile opens, creates or truncates a file. The permission argument is
// ignored (see Options.FileMode).
func (f *FS) OpenFile(filename string, flag int, _ os.FileMode) (billy.File, error) {
	const op = "open"
	rel := cleanRel(filename)
	writable := flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0
	if writable && f.opts.ReadOnly {
		return nil, pathErr(op, filename, syscall.EROFS)
	}
	if rel == "" {
		return nil, pathErr(op, filename, syscall.EISDIR)
	}
	ctx, cancel := f.ctx()
	defer cancel()

	var fh *file
	err := retry(ctx, op, filename, func() (bool, error) {
		e, err := f.lookup(ctx, rel)
		switch {
		case err != nil:
			return false, pathErr(op, filename, err)
		case e.isDir:
			return false, pathErr(op, filename, syscall.EISDIR)
		case !e.exists && flag&os.O_CREATE == 0:
			return false, pathErr(op, filename, os.ErrNotExist)
		case e.exists && flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0:
			return false, pathErr(op, filename, os.ErrExist)
		}

		key := f.fileKey(rel)
		var cmps []clientv3.Cmp
		switch {
		case !e.exists: // create
			if f.denied(rel) {
				return false, pathErr(op, filename, os.ErrPermission)
			}
			if err := f.requireDir(ctx, op, filename, parentRel(rel)); err != nil {
				return false, err
			}
			cmps = []clientv3.Cmp{
				clientv3.Compare(clientv3.CreateRevision(key), "=", 0),
				clientv3.Compare(clientv3.CreateRevision(f.dirKey(rel)), "=", 0).WithPrefix(),
			}
		case flag&os.O_TRUNC != 0 && writable && len(e.kv.Value) > 0: // truncate
			cmps = []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(key), "=", e.kv.ModRevision)}
		default: // plain open
			fh = f.newFile(filename, rel, flag, e.kv.Value)
			return true, nil
		}

		ok, err := f.commit(ctx, cmps, clientv3.OpPut(key, "", withLease(e.kv)...))
		if err != nil {
			return false, pathErr(op, filename, err)
		}
		if ok {
			fh = f.newFile(filename, rel, flag, nil)
		}
		return ok, nil
	})
	if err != nil {
		return nil, err
	}
	return fh, nil
}

func (f *FS) TempFile(dir, prefix string) (billy.File, error) {
	for range 8 {
		name := path.Join(dir, fmt.Sprintf("%s%012x", prefix, rand.Uint64()&(1<<48-1)))
		fh, err := f.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, os.ErrExist) {
			return fh, err
		}
	}
	return nil, pathErr("tempfile", dir, os.ErrExist)
}
