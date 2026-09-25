package main

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// node is a loopback node over the git work tree. Every operation that can
// change the tree goes through the Gate; everything else is inherited from
// fs.LoopbackNode unchanged.
type node struct {
	*fs.LoopbackNode
	gate *Gate
}

func NewRootNode(workTree string, gate *Gate) (*node, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(workTree, &st); err != nil {
		return nil, err
	}
	rd := &fs.LoopbackRoot{Path: workTree, Dev: uint64(st.Dev)}
	root := &node{LoopbackNode: &fs.LoopbackNode{RootData: rd}, gate: gate}
	rd.RootNode = root
	return root, nil
}

func MountOptions(cfg *Config) *fs.Options {
	et := cfg.Mount.EntryTimeout.Duration
	at := cfg.Mount.AttrTimeout.Duration
	return &fs.Options{
		EntryTimeout:    &et,
		AttrTimeout:     &at,
		NegativeTimeout: &et,
		MountOptions: fuse.MountOptions{
			// Deliberately not the repo URL: it may embed credentials
			// and would be world-readable in /proc/mounts.
			FsName:     "gitmount",
			Name:       "gitmount",
			AllowOther: cfg.Mount.AllowOther,
			Debug:      cfg.Mount.Debug,
			// git does not track xattrs; keep the surface small.
			DisableXAttrs: true,
			// Let the kernel enforce mode bits against the *caller*.
			// Without this, with allow_other every user would act with
			// the daemon's privileges.
			Options: []string{"default_permissions"},
		},
	}
}

var (
	_ fs.NodeWrapChilder    = (*node)(nil)
	_ fs.NodeOpener         = (*node)(nil)
	_ fs.NodeCreater        = (*node)(nil)
	_ fs.NodeMkdirer        = (*node)(nil)
	_ fs.NodeMknoder        = (*node)(nil)
	_ fs.NodeSymlinker      = (*node)(nil)
	_ fs.NodeLinker         = (*node)(nil)
	_ fs.NodeUnlinker       = (*node)(nil)
	_ fs.NodeRmdirer        = (*node)(nil)
	_ fs.NodeRenamer        = (*node)(nil)
	_ fs.NodeSetattrer      = (*node)(nil)
	_ fs.NodeCopyFileRanger = (*node)(nil)
)

func (n *node) WrapChild(ctx context.Context, ops fs.InodeEmbedder) fs.InodeEmbedder {
	return &node{LoopbackNode: ops.(*fs.LoopbackNode), gate: n.gate}
}

// isDotGit matches ".git" case-insensitively; git refuses such paths in the
// index, so allowing them would make every subsequent commit fail. Blocking
// it everywhere also prevents nested repositories.
func isDotGit(name string) bool { return strings.EqualFold(name, ".git") }

// isReserved reports names that may not be created through the mount:
// ".git", and gitmount's own temp-file prefix.
func isReserved(name string) bool { return isDotGit(name) || strings.HasPrefix(name, tmpPrefix) }

// git refuses these names as symlinks (CVE-2018-11235 and follow-ups).
func isNoSymlinkName(name string) bool {
	for _, s := range []string{".gitmodules", ".gitattributes", ".gitignore", ".mailmap"} {
		if strings.EqualFold(name, s) {
			return true
		}
	}
	return false
}

func (n *node) backingPath(name string) string {
	return filepath.Join(n.RootData.Path, n.Path(nil), name)
}

func isWriteOpen(flags uint32) bool {
	return flags&syscall.O_ACCMODE != syscall.O_RDONLY || flags&syscall.O_TRUNC != 0
}

func (n *node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if !isWriteOpen(flags) {
		fh, ff, errno := n.LoopbackNode.Open(ctx, flags)
		if errno != 0 {
			return nil, 0, errno
		}
		return wrapFileFlags(fh, ff, n.gate, false)
	}
	var (
		fh    fs.FileHandle
		ff    uint32
		errno syscall.Errno
	)
	n.gate.Mutate(func() syscall.Errno {
		fh, ff, errno = n.LoopbackNode.Open(ctx, flags)
		if errno == 0 {
			// Counted while holding the shared lock, so the syncer
			// sees a stable count under its exclusive lock.
			n.gate.addWriter()
		}
		return errno
	})
	if errno != 0 {
		return nil, 0, errno
	}
	return wrapFileFlags(fh, ff, n.gate, true)
}

func (n *node) Create(ctx context.Context, name string, flags, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if isReserved(name) {
		return nil, nil, 0, syscall.EPERM
	}
	var (
		ino   *fs.Inode
		fh    fs.FileHandle
		ff    uint32
		errno syscall.Errno
	)
	n.gate.Mutate(func() syscall.Errno {
		ino, fh, ff, errno = n.LoopbackNode.Create(ctx, name, flags, mode, out)
		if errno == 0 {
			n.gate.addWriter()
		}
		return errno
	})
	if errno != 0 {
		return nil, nil, 0, errno
	}
	wfh, wff, errno := wrapFileFlags(fh, ff, n.gate, true)
	return ino, wfh, wff, errno
}

func (n *node) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (ino *fs.Inode, errno syscall.Errno) {
	if isReserved(name) {
		return nil, syscall.EPERM
	}
	n.gate.Mutate(func() syscall.Errno {
		ino, errno = n.LoopbackNode.Mkdir(ctx, name, mode, out)
		return errno
	})
	return ino, errno
}

func (n *node) Mknod(ctx context.Context, name string, mode, rdev uint32, out *fuse.EntryOut) (ino *fs.Inode, errno syscall.Errno) {
	if isReserved(name) {
		return nil, syscall.EPERM
	}
	n.gate.Mutate(func() syscall.Errno {
		ino, errno = n.LoopbackNode.Mknod(ctx, name, mode, rdev, out)
		return errno
	})
	return ino, errno
}

func (n *node) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (ino *fs.Inode, errno syscall.Errno) {
	if isReserved(name) || isNoSymlinkName(name) {
		return nil, syscall.EPERM
	}
	n.gate.Mutate(func() syscall.Errno {
		ino, errno = n.LoopbackNode.Symlink(ctx, target, name, out)
		return errno
	})
	return ino, errno
}

func (n *node) Link(ctx context.Context, target fs.InodeEmbedder, name string, out *fuse.EntryOut) (ino *fs.Inode, errno syscall.Errno) {
	if isReserved(name) {
		return nil, syscall.EPERM
	}
	n.gate.Mutate(func() syscall.Errno {
		ino, errno = n.LoopbackNode.Link(ctx, target, name, out)
		return errno
	})
	return ino, errno
}

func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.gate.Mutate(func() syscall.Errno { return n.LoopbackNode.Unlink(ctx, name) })
}

func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	return n.gate.Mutate(func() syscall.Errno { return n.LoopbackNode.Rmdir(ctx, name) })
}

const renameExchange = 0x2 // RENAME_EXCHANGE

func (n *node) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if isReserved(newName) {
		return syscall.EPERM
	}
	if flags&renameExchange != 0 {
		// Both entries get new names; keep the rule simple.
		if isReserved(name) || isNoSymlinkName(name) || isNoSymlinkName(newName) {
			return syscall.EPERM
		}
	} else if isNoSymlinkName(newName) {
		var st syscall.Stat_t
		if err := syscall.Lstat(n.backingPath(name), &st); err != nil {
			return fs.ToErrno(err)
		}
		if st.Mode&syscall.S_IFMT == syscall.S_IFLNK {
			return syscall.EPERM
		}
	}
	return n.gate.Mutate(func() syscall.Errno {
		return n.LoopbackNode.Rename(ctx, name, newParent, newName, flags)
	})
}

func (n *node) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	// Unwrap our handle so LoopbackNode forwards to the real file. The
	// shared lock is taken here, once; the file-level Setattr never locks.
	if wf, ok := f.(*file); ok {
		f = wf.lf
	}
	return n.gate.Mutate(func() syscall.Errno {
		return n.LoopbackNode.Setattr(ctx, f, in, out)
	})
}

// CopyFileRange is disabled. ENOSYS makes the kernel stop sending it and
// fall back to ordinary reads and writes, which go through the Gate.
func (n *node) CopyFileRange(ctx context.Context, fhIn fs.FileHandle, offIn uint64, out *fs.Inode, fhOut fs.FileHandle, offOut uint64, length uint64, flags uint64) (uint32, syscall.Errno) {
	return 0, syscall.ENOSYS
}

// file wraps a LoopbackFile, forwarding an explicit allowlist of
// operations. It intentionally does NOT embed *fs.LoopbackFile: that would
// expose PassthroughFd (kernel FUSE passthrough, auto-enabled when running
// as root on newer kernels), letting writes bypass the Gate, and Ioctl.
type file struct {
	lf       *fs.LoopbackFile
	gate     *Gate
	writable bool
}

var (
	_ fs.FileReader    = (*file)(nil)
	_ fs.FileWriter    = (*file)(nil)
	_ fs.FileFlusher   = (*file)(nil)
	_ fs.FileFsyncer   = (*file)(nil)
	_ fs.FileReleaser  = (*file)(nil)
	_ fs.FileGetattrer = (*file)(nil)
	_ fs.FileSetattrer = (*file)(nil)
	_ fs.FileLseeker   = (*file)(nil)
	_ fs.FileAllocater = (*file)(nil)
)

func wrapFileFlags(fh fs.FileHandle, ff uint32, gate *Gate, writable bool) (fs.FileHandle, uint32, syscall.Errno) {
	lf, ok := fh.(*fs.LoopbackFile)
	if !ok {
		// Should be impossible; fail closed rather than bypass the Gate.
		if r, ok := fh.(fs.FileReleaser); ok {
			r.Release(context.Background())
		}
		if writable {
			gate.dropWriter()
		}
		return nil, 0, syscall.EIO
	}
	return &file{lf: lf, gate: gate, writable: writable}, ff, 0
}

func (f *file) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	return f.lf.Read(ctx, dest, off)
}

func (f *file) Write(ctx context.Context, data []byte, off int64) (n uint32, errno syscall.Errno) {
	f.gate.Mutate(func() syscall.Errno {
		n, errno = f.lf.Write(ctx, data, off)
		return errno
	})
	return n, errno
}

func (f *file) Allocate(ctx context.Context, off, size uint64, mode uint32) syscall.Errno {
	return f.gate.Mutate(func() syscall.Errno { return f.lf.Allocate(ctx, off, size, mode) })
}

func (f *file) Flush(ctx context.Context) syscall.Errno { return f.lf.Flush(ctx) }

func (f *file) Fsync(ctx context.Context, flags uint32) syscall.Errno { return f.lf.Fsync(ctx, flags) }

func (f *file) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	return f.lf.Getattr(ctx, out)
}

// Setattr is only reached via node.Setattr, which already holds the lock.
func (f *file) Setattr(ctx context.Context, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	return f.lf.Setattr(ctx, in, out)
}

func (f *file) Lseek(ctx context.Context, off uint64, whence uint32) (uint64, syscall.Errno) {
	return f.lf.Lseek(ctx, off, whence)
}

func (f *file) Release(ctx context.Context) syscall.Errno {
	if !f.writable {
		return f.lf.Release(ctx)
	}
	return f.gate.Mutate(func() syscall.Errno {
		defer f.gate.dropWriter()
		return f.lf.Release(ctx)
	})
}
