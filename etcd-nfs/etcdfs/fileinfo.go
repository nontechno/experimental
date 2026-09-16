package etcdfs

import (
	"hash/fnv"
	"os"
	"time"

	nfsfile "github.com/willscott/go-nfs/file"
)

type fileInfo struct {
	name  string
	size  int64
	mode  os.FileMode
	mtime time.Time
	sys   nfsfile.FileInfo
}

func (fi *fileInfo) Name() string       { return fi.name }
func (fi *fileInfo) Size() int64        { return fi.size }
func (fi *fileInfo) Mode() os.FileMode  { return fi.mode }
func (fi *fileInfo) ModTime() time.Time { return fi.mtime }
func (fi *fileInfo) IsDir() bool        { return fi.mode.IsDir() }

// Sys returns the structure go-nfs reads uid/gid/nlink/fileid from.
func (fi *fileInfo) Sys() any { return fi.sys }

// fileID derives a stable NFS fileid from the path relative to the top-level
// root. etcd has no inode numbers; hashing gives the same id from Stat and
// ReadDir, from every mount view, and across restarts.
func fileID(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	if id := h.Sum64(); id != 0 {
		return id
	}
	return 1
}

// abs is the path relative to the top-level root.
func (f *FS) newFileInfo(abs string, size, modRev, headerRev int64) *fileInfo {
	return &fileInfo{
		name:  baseName(abs),
		size:  size,
		mode:  f.opts.FileMode.Perm(),
		mtime: f.clock.timeOf(modRev, headerRev),
		sys:   nfsfile.FileInfo{Nlink: 1, UID: f.opts.UID, GID: f.opts.GID, Fileid: fileID(abs)},
	}
}

// newDirInfo: a directory's mtime is the time of the latest store revision
// seen. etcd cannot tell cheaply when a subtree last changed (deletes leave no
// trace in a range), so any write advances it; a stale listing is never
// treated as fresh.
func (f *FS) newDirInfo(abs string, headerRev int64) *fileInfo {
	return &fileInfo{
		name:  baseName(abs),
		size:  4096,
		mode:  os.ModeDir | f.opts.DirMode.Perm(),
		mtime: f.clock.timeOf(headerRev, headerRev),
		sys:   nfsfile.FileInfo{Nlink: 2, UID: f.opts.UID, GID: f.opts.GID, Fileid: fileID(abs + "/")},
	}
}
