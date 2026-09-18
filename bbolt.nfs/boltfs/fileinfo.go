package boltfs

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

// Sys carries the uid/gid/nlink/fileid that go-nfs puts in NFS attributes.
func (fi *fileInfo) Sys() any { return fi.sys }

// fileID derives an NFS fileid from the path from the database root. bbolt has
// no inode numbers; hashing the path gives the same id from Stat, ReadDir and
// across restarts, which is what clients need to keep handles coherent.
// Directories hash a trailing slash so a file and a directory of the same
// name in different places cannot collide by construction.
func fileID(abs string, isDir bool) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(abs))
	if isDir {
		_, _ = h.Write([]byte{'/'})
	}
	if id := h.Sum64(); id != 0 {
		return id
	}
	return 1
}

func (f *FS) newFileInfo(name, abs string, size int64) *fileInfo {
	return &fileInfo{
		name:  name,
		size:  size,
		mode:  f.opts.FileMode.Perm(),
		mtime: f.times.get(abs),
		sys: nfsfile.FileInfo{
			Nlink:  1,
			UID:    f.opts.UID,
			GID:    f.opts.GID,
			Fileid: fileID(abs, false),
		},
	}
}

func (f *FS) newDirInfo(name, abs string) *fileInfo {
	return &fileInfo{
		name:  name,
		size:  4096,
		mode:  os.ModeDir | f.opts.DirMode.Perm(),
		mtime: f.times.get(abs),
		sys: nfsfile.FileInfo{
			Nlink:  2,
			UID:    f.opts.UID,
			GID:    f.opts.GID,
			Fileid: fileID(abs, true),
		},
	}
}
