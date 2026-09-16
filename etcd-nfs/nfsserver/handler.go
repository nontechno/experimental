// Package nfsserver serves an etcdfs.FS over NFSv3 using go-nfs.
package nfsserver

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"time"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"

	"github.com/nontechno/experimental/etcd-nfs/etcdfs"
)

// Options configures a Handler.
type Options struct {
	// QuotaBytes is reported as the filesystem size when etcd does not
	// report its backend quota. Default 2 GiB (etcd's default quota).
	QuotaBytes int64
	// CacheSize bounds go-nfs's cache of long-path handles and directory
	// listing verifiers. Default 4096.
	CacheSize int
}

// Handler implements nfs.Handler for an etcdfs.FS. Clients may mount "/" or
// any directory below it.
type Handler struct {
	root  *etcdfs.FS
	opts  Options
	cache cachingHandler // long-path handles and READDIR verifiers
}

type cachingHandler interface {
	nfs.Handler
	nfs.CachingHandler
}

var _ nfs.CachingHandler = (*Handler)(nil)

// New returns a handler exposing root.
func New(root *etcdfs.FS, opts Options) *Handler {
	if opts.QuotaBytes <= 0 {
		opts.QuotaBytes = 2 << 30
	}
	if opts.CacheSize <= 0 {
		opts.CacheSize = 4096
	}
	h := &Handler{root: root, opts: opts}
	h.cache = nfshelper.NewCachingHandler(delegate{h}, opts.CacheSize).(cachingHandler)
	return h
}

func (h *Handler) Mount(_ context.Context, conn net.Conn, req nfs.MountRequest) (nfs.MountStatus, billy.Filesystem, []nfs.AuthFlavor) {
	dir := string(req.Dirpath)
	flavors := []nfs.AuthFlavor{nfs.AuthFlavorNull}
	st, err := h.root.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		nfs.Log.Infof("mount %q from %s: no such directory", dir, conn.RemoteAddr())
		return nfs.MountStatusErrNoEnt, nil, flavors
	case err != nil:
		nfs.Log.Errorf("mount %q from %s: %v", dir, conn.RemoteAddr(), err)
		return nfs.MountStatusErrIO, nil, flavors
	case !st.IsDir():
		return nfs.MountStatusErrNotDir, nil, flavors
	}
	view, err := h.root.Chroot(dir)
	if err != nil {
		return nfs.MountStatusErrServerFault, nil, flavors
	}
	nfs.Log.Infof("mount %q from %s -> etcd prefix %q", dir, conn.RemoteAddr(), view.Root())
	return nfs.MountStatusOk, view, flavors
}

func (h *Handler) Change(f billy.Filesystem) billy.Change {
	c, _ := f.(billy.Change)
	return c
}

// FSStat reports etcd backend usage against its quota, so df and Finder's
// free-space checks see real numbers.
func (h *Handler) FSStat(ctx context.Context, _ billy.Filesystem, s *nfs.FSStat) error {
	cli := h.root.Client()
	quota, used := h.opts.QuotaBytes, int64(0)
	for _, ep := range cli.Endpoints() {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		st, err := cli.Status(cctx, ep)
		cancel()
		if err != nil {
			continue
		}
		used = st.DbSize
		if st.DbSizeQuota > 0 {
			quota = st.DbSizeQuota
		}
		break
	}
	free := uint64(max(quota-used, 0))
	s.TotalSize, s.FreeSize = uint64(quota), free
	if s.AvailableSize != 0 { // go-nfs zeroes it for read-only filesystems
		s.AvailableSize = free
	}
	s.TotalFiles, s.FreeFiles = 1<<32, 1<<32
	if s.AvailableFiles != 0 {
		s.AvailableFiles = 1 << 32
	}
	return nil
}

func (h *Handler) VerifierFor(path string, contents []fs.FileInfo) uint64 {
	return h.cache.VerifierFor(path, contents)
}

func (h *Handler) DataForVerifier(path string, verifier uint64) []fs.FileInfo {
	return h.cache.DataForVerifier(path, verifier)
}

// delegate is the handler wrapped by go-nfs's caching helper, which needs a
// complete nfs.Handler. Only the helper's own handle cache is used.
type delegate struct{ h *Handler }

func (d delegate) Mount(ctx context.Context, c net.Conn, r nfs.MountRequest) (nfs.MountStatus, billy.Filesystem, []nfs.AuthFlavor) {
	return d.h.Mount(ctx, c, r)
}
func (d delegate) Change(f billy.Filesystem) billy.Change { return d.h.Change(f) }
func (d delegate) FSStat(ctx context.Context, f billy.Filesystem, s *nfs.FSStat) error {
	return d.h.FSStat(ctx, f, s)
}
func (delegate) ToHandle(billy.Filesystem, []string) []byte            { return nil }
func (delegate) FromHandle([]byte) (billy.Filesystem, []string, error) { return nil, nil, nil }
func (delegate) InvalidateHandle(billy.Filesystem, []byte) error       { return nil }
func (delegate) HandleLimit() int                                      { return -1 }
