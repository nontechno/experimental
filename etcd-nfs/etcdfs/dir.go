package etcdfs

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"syscall"

	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const readDirPageSize = 1000

// ReadDir lists the direct children of a directory, sorted by name.
//
// It scans the directory prefix keys-only in pages pinned to one revision and
// jumps over each subdirectory's subtree, so the cost is proportional to the
// number of children rather than descendants. File sizes then come from
// batched point reads at the same revision.
func (f *FS) ReadDir(dirname string) ([]os.FileInfo, error) {
	rel := cleanRel(dirname)
	ctx, cancel := f.ctx()
	defer cancel()
	for attempt := 0; ; attempt++ {
		infos, err := f.readDir(ctx, rel)
		if errors.Is(err, rpctypes.ErrCompacted) && attempt < 2 {
			continue // pinned revision was compacted mid-listing
		}
		if err != nil {
			return nil, pathErr("readdir", dirname, err)
		}
		return infos, nil
	}
}

func (f *FS) readDir(ctx context.Context, rel string) ([]os.FileInfo, error) {
	e, err := f.lookup(ctx, rel)
	switch {
	case err != nil:
		return nil, err
	case !e.exists:
		return nil, os.ErrNotExist
	case !e.isDir:
		return nil, syscall.ENOTDIR
	}

	children, fileKeys, rev, err := f.listChildren(ctx, f.dirKey(rel))
	if err != nil {
		return nil, err
	}
	kvs, err := f.getAt(ctx, fileKeys, rev)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(children))
	for n := range children {
		names = append(names, n)
	}
	sort.Strings(names)

	infos := make([]os.FileInfo, 0, len(names))
	for _, name := range names {
		childRel := name
		if rel != "" {
			childRel = rel + "/" + name
		}
		abs := f.absRel(childRel)
		if key := children[name]; key == "" {
			infos = append(infos, f.newDirInfo(abs, rev))
		} else if kv, ok := kvs[key]; ok {
			infos = append(infos, f.newFileInfo(abs, int64(len(kv.Value)), kv.ModRevision, rev))
		}
	}
	return infos, nil
}

// listChildren maps each child name of the directory prefix dk to its file
// key, or to "" for subdirectories (which win over a same-named file). It
// returns the file keys to fetch and the revision the listing is pinned to.
func (f *FS) listChildren(ctx context.Context, dk string) (map[string]string, []string, int64, error) {
	end := clientv3.GetPrefixRangeEnd(dk)
	children := map[string]string{}
	start, rev := dk, int64(0)
	for {
		opts := []clientv3.OpOption{clientv3.WithRange(end), clientv3.WithKeysOnly(), clientv3.WithLimit(readDirPageSize)}
		if rev != 0 {
			opts = append(opts, clientv3.WithRev(rev))
		}
		resp, err := f.cli.Get(ctx, start, opts...)
		if err != nil {
			return nil, nil, 0, err
		}
		if rev == 0 {
			rev = resp.Header.Revision
			f.clock.observe(rev)
		}

		lastKey, skip := "", ""
		for _, kv := range resp.Kvs {
			k := string(kv.Key)
			lastKey = k
			if skip != "" && strings.HasPrefix(k, skip) {
				continue // inside a subdirectory already recorded
			}
			name, _, deeper := strings.Cut(k[len(dk):], "/")
			if deeper {
				skip = dk + name + "/"
			}
			switch {
			case !validName(name): // own marker (""), or unrepresentable key
			case deeper:
				children[name] = ""
			default:
				if _, seen := children[name]; !seen {
					children[name] = k
				}
			}
		}
		if !resp.More || lastKey == "" {
			break
		}
		if skip != "" && strings.HasPrefix(lastKey, skip) {
			start = clientv3.GetPrefixRangeEnd(skip) // jump past the subtree
		} else {
			start = lastKey + "\x00"
		}
		if end != "\x00" && start >= end {
			break
		}
	}

	var fileKeys []string
	for _, key := range children {
		if key != "" {
			fileKeys = append(fileKeys, key)
		}
	}
	sort.Strings(fileKeys)
	return children, fileKeys, rev, nil
}

// getAt reads keys at revision rev in batched read-only transactions.
func (f *FS) getAt(ctx context.Context, keys []string, rev int64) (map[string]*mvccpb.KeyValue, error) {
	out := make(map[string]*mvccpb.KeyValue, len(keys))
	batch := min(f.opts.MaxTxnOps, 64)
	for len(keys) > 0 {
		n := min(batch, len(keys))
		ops := make([]clientv3.Op, n)
		for i, k := range keys[:n] {
			ops[i] = clientv3.OpGet(k, clientv3.WithRev(rev))
		}
		keys = keys[n:]
		resp, err := f.cli.Txn(ctx).Then(ops...).Commit()
		if err != nil {
			return nil, err
		}
		for _, r := range resp.Responses {
			for _, kv := range r.GetResponseRange().Kvs {
				out[string(kv.Key)] = kv
			}
		}
	}
	return out, nil
}

// MkdirAll creates a directory marker for filename and any missing parents.
// The permission argument is ignored (see Options.DirMode).
func (f *FS) MkdirAll(filename string, _ os.FileMode) error {
	if f.opts.ReadOnly {
		return pathErr("mkdir", filename, syscall.EROFS)
	}
	ctx, cancel := f.ctx()
	defer cancel()
	return f.mkdirAll(ctx, filename, cleanRel(filename))
}

func (f *FS) mkdirAll(ctx context.Context, name, rel string) error {
	const op = "mkdir"
	if rel == "" {
		return nil
	}
	return retry(ctx, op, name, func() (bool, error) {
		e, err := f.lookup(ctx, rel)
		switch {
		case err != nil:
			return false, pathErr(op, name, err)
		case e.isDir:
			return true, nil
		case e.exists:
			return false, pathErr(op, name, syscall.ENOTDIR)
		case f.denied(rel):
			return false, pathErr(op, name, os.ErrPermission)
		}
		if err := f.mkdirAll(ctx, name, parentRel(rel)); err != nil {
			return false, err
		}
		ok, err := f.commit(ctx,
			[]clientv3.Cmp{clientv3.Compare(clientv3.CreateRevision(f.fileKey(rel)), "=", 0)},
			clientv3.OpPut(f.dirKey(rel), ""))
		if err != nil {
			return false, pathErr(op, name, err)
		}
		return ok, nil
	})
}

// Remove deletes a file, or a directory that has no entries.
func (f *FS) Remove(filename string) error {
	const op = "remove"
	if f.opts.ReadOnly {
		return pathErr(op, filename, syscall.EROFS)
	}
	rel := cleanRel(filename)
	if rel == "" {
		return pathErr(op, filename, syscall.EBUSY)
	}
	ctx, cancel := f.ctx()
	defer cancel()

	return retry(ctx, op, filename, func() (bool, error) {
		e, err := f.lookup(ctx, rel)
		switch {
		case err != nil:
			return false, pathErr(op, filename, err)
		case !e.exists:
			return false, pathErr(op, filename, os.ErrNotExist)
		}
		keep := f.keepDir(parentRel(rel))

		if !e.isDir {
			key := f.fileKey(rel)
			ok, err := f.commit(ctx,
				[]clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(key), "=", e.kv.ModRevision)},
				append([]clientv3.Op{clientv3.OpDelete(key)}, keep...)...)
			if err != nil {
				return false, pathErr(op, filename, err)
			}
			return ok, nil
		}

		// A directory may only be removed while nothing but its marker exists.
		dk := f.dirKey(rel)
		ok, err := f.commit(ctx,
			[]clientv3.Cmp{noKeysUnder(dk)},
			append([]clientv3.Op{clientv3.OpDelete(dk)}, keep...)...)
		switch {
		case err != nil:
			return false, pathErr(op, filename, err)
		case !ok:
			return false, pathErr(op, filename, syscall.ENOTEMPTY)
		}
		return true, nil
	})
}

// noKeysUnder compares true when no key exists strictly inside the directory
// prefix dk (the marker dk itself is allowed). A range compare over an empty
// range evaluates against a zero KeyValue, so CreateRevision == 0 holds.
func noKeysUnder(dk string) clientv3.Cmp {
	return clientv3.Compare(clientv3.CreateRevision(dk+"\x00"), "=", 0).WithRange(clientv3.GetPrefixRangeEnd(dk))
}
