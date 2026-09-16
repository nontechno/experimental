package etcdfs

import (
	"context"
	"os"
	"strings"
	"syscall"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// Rename moves a file or directory, replacing an existing destination file
// or empty destination directory.
func (f *FS) Rename(oldpath, newpath string) error {
	const op = "rename"
	if f.opts.ReadOnly {
		return pathErr(op, oldpath, syscall.EROFS)
	}
	from, to := cleanRel(oldpath), cleanRel(newpath)
	switch {
	case from == to:
		return nil
	case from == "" || to == "":
		return pathErr(op, oldpath, syscall.EBUSY)
	case strings.HasPrefix(to, from+"/"): // into itself
		return pathErr(op, oldpath, syscall.EINVAL)
	case strings.HasPrefix(from, to+"/"): // onto an ancestor, which is never empty
		return pathErr(op, newpath, syscall.ENOTEMPTY)
	case f.denied(to):
		return pathErr(op, newpath, os.ErrPermission)
	}
	ctx, cancel := f.ctx()
	defer cancel()

	return retry(ctx, op, oldpath, func() (bool, error) {
		src, err := f.lookup(ctx, from)
		switch {
		case err != nil:
			return false, pathErr(op, oldpath, err)
		case !src.exists:
			return false, pathErr(op, oldpath, os.ErrNotExist)
		}
		if err := f.requireDir(ctx, op, newpath, parentRel(to)); err != nil {
			return false, err
		}
		dst, err := f.lookup(ctx, to)
		if err != nil {
			return false, pathErr(op, newpath, err)
		}
		if src.isDir {
			return f.renameDir(ctx, oldpath, newpath, src, dst)
		}
		return f.renameFile(ctx, oldpath, newpath, src, dst)
	})
}

func (f *FS) renameFile(ctx context.Context, oldpath, newpath string, src, dst *entry) (bool, error) {
	if dst.isDir {
		return false, pathErr("rename", newpath, syscall.EISDIR)
	}
	from, to := f.fileKey(src.rel), f.fileKey(dst.rel)
	cmps := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(from), "=", src.kv.ModRevision),
		clientv3.Compare(clientv3.CreateRevision(f.dirKey(dst.rel)), "=", 0).WithPrefix(),
	}
	if dst.exists {
		cmps = append(cmps, clientv3.Compare(clientv3.ModRevision(to), "=", dst.kv.ModRevision))
	} else {
		cmps = append(cmps, clientv3.Compare(clientv3.CreateRevision(to), "=", 0))
	}
	ops := []clientv3.Op{
		clientv3.OpPut(to, string(src.kv.Value), withLease(src.kv)...),
		clientv3.OpDelete(from),
	}
	ops = append(ops, f.keepSourceParent(src.rel, dst.rel)...)
	ok, err := f.commit(ctx, cmps, ops...)
	if err != nil {
		return false, pathErr("rename", oldpath, err)
	}
	return ok, nil
}

// renameDir moves a subtree in one transaction: every key under the source
// prefix is re-put under the destination prefix and the source range is
// deleted, so the subtree must fit in one transaction.
//
// The guard detects keys created or modified under the source after it was
// listed. etcd compares cannot detect a key deleted from a range, so a delete
// racing a directory rename can be resurrected at the destination.
func (f *FS) renameDir(ctx context.Context, oldpath, newpath string, src, dst *entry) (bool, error) {
	const op = "rename"
	if dst.exists && !dst.isDir {
		return false, pathErr(op, newpath, syscall.ENOTDIR)
	}
	fromDir, toDir := f.dirKey(src.rel), f.dirKey(dst.rel)

	list, err := f.cli.Get(ctx, fromDir, clientv3.WithPrefix(), clientv3.WithLimit(int64(f.opts.MaxTxnOps)))
	if err != nil {
		return false, pathErr(op, oldpath, err)
	}
	rev := list.Header.Revision
	f.clock.observe(rev)
	if len(list.Kvs) == 0 {
		return false, nil // vanished since lookup; retry
	}
	if list.More {
		return false, pathErr(op, oldpath, ErrTooLarge)
	}

	ops := []clientv3.Op{clientv3.OpPut(toDir, "")} // destination marker
	size := len(toDir)
	for _, kv := range list.Kvs {
		k := string(kv.Key)
		if k == fromDir {
			continue // source marker; replaced by the destination marker
		}
		newKey := toDir + k[len(fromDir):]
		ops = append(ops, clientv3.OpPut(newKey, string(kv.Value), withLease(kv)...))
		size += len(newKey) + len(kv.Value)
	}
	ops = append(ops, clientv3.OpDelete(fromDir, clientv3.WithPrefix()))
	ops = append(ops, f.keepSourceParent(src.rel, dst.rel)...)
	if len(ops) > f.opts.MaxTxnOps || size+requestOverhead > f.opts.MaxRequestBytes {
		return false, pathErr(op, oldpath, ErrTooLarge)
	}

	cmps := []clientv3.Cmp{
		// nothing under the source was created or modified since the listing
		clientv3.Compare(clientv3.ModRevision(fromDir), "<", rev+1).WithPrefix(),
		// the destination is not a file, and holds nothing but its marker
		clientv3.Compare(clientv3.CreateRevision(f.fileKey(dst.rel)), "=", 0),
		noKeysUnder(toDir),
	}
	ok, err := f.commit(ctx, cmps, ops...)
	if err != nil {
		return false, pathErr(op, oldpath, err)
	}
	if !ok && f.hasChildren(ctx, dst.rel) {
		return false, pathErr(op, newpath, syscall.ENOTEMPTY)
	}
	return ok, nil
}

// keepSourceParent keeps the source's parent directory alive when a move
// takes its last entry, unless the destination lies inside that parent.
func (f *FS) keepSourceParent(from, to string) []clientv3.Op {
	parent := parentRel(from)
	if parent == "" || to == parent || strings.HasPrefix(to, parent+"/") {
		return nil
	}
	return f.keepDir(parent)
}

func (f *FS) hasChildren(ctx context.Context, rel string) bool {
	dk := f.dirKey(rel)
	resp, err := f.cli.Get(ctx, dk+"\x00", clientv3.WithRange(clientv3.GetPrefixRangeEnd(dk)), clientv3.WithCountOnly())
	return err == nil && resp.Count > 0
}
