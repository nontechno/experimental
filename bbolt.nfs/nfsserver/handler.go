// Package nfsserver adapts a boltfs.FS to the go-nfs Handler interface.
package nfsserver

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"strings"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"

	"github.com/sport/bbolt-nfs/boltfs"
)

// maxHandle is the NFSv3 file handle size limit. Handles are always a
// multiple of 4 bytes long: an NFS handle is an XDR opaque, and the decoder
// go-nfs uses reads the declared length without skipping the padding that
// follows it, so a handle whose length is not a multiple of 4 shifts every
// field parsed after it in the same request. ACCESS is the first casualty —
// its mask decodes as garbage and the client is told it may do nothing,
// which surfaces as "permission denied" on a directory that lists fine.
const maxHandle = 64

// Handle layout: [kind][length][payload...][zero padding to a multiple of 4].
// The explicit length keeps decoding exact, so padding never has to be
// guessed at.
const (
	handlePath   byte = 'P' // payload: slash-joined path from the export root
	handleCached byte = 'H' // payload: go-nfs UUID, for paths too long to encode
	handleHeader      = 2
	maxPayload        = maxHandle - handleHeader
)

// encodeHandle frames a payload and pads the result to a multiple of 4 bytes.
func encodeHandle(kind byte, payload []byte) []byte {
	size := (handleHeader + len(payload) + 3) &^ 3
	fh := make([]byte, size)
	fh[0], fh[1] = kind, byte(len(payload))
	copy(fh[handleHeader:], payload)
	return fh
}

// decodeHandle returns the payload, or ok=false if the handle is malformed.
func decodeHandle(fh []byte) (kind byte, payload []byte, ok bool) {
	if len(fh) < handleHeader || len(fh)%4 != 0 {
		return 0, nil, false
	}
	n := int(fh[1])
	if handleHeader+n > len(fh) {
		return 0, nil, false
	}
	return fh[0], fh[handleHeader : handleHeader+n], true
}

// Options configures the handler.
type Options struct {
	// HandleCacheSize bounds the fallback handle cache and the
	// directory-listing verifier cache. Default 4096.
	HandleCacheSize int
}

// Handler serves a boltfs.FS over NFSv3.
//
// Handles for paths up to 63 bytes encode the path itself, so they stay valid
// across a server restart and an existing mount does not go stale. Longer
// paths fall back to go-nfs's in-memory UUID cache, which does not survive a
// restart.
type Handler struct {
	root  *boltfs.FS
	inner nfs.Handler // caching handler: long-path handles and readdir verifiers
}

var (
	_ nfs.Handler        = (*Handler)(nil)
	_ nfs.CachingHandler = (*Handler)(nil)
)

// New returns a handler exposing root. Clients may mount "/" or any directory
// below it, e.g. 127.0.0.1:/config/tls.
func New(root *boltfs.FS, opts Options) *Handler {
	if opts.HandleCacheSize <= 0 {
		opts.HandleCacheSize = 4096
	}
	h := &Handler{root: root}
	h.inner = nfshelper.NewCachingHandler(&base{h}, opts.HandleCacheSize)
	return h
}

func (h *Handler) Mount(_ context.Context, conn net.Conn, req nfs.MountRequest) (nfs.MountStatus, billy.Filesystem, []nfs.AuthFlavor) {
	dir := string(req.Dirpath)
	flavors := []nfs.AuthFlavor{nfs.AuthFlavorNull}
	st, err := h.root.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		nfs.Log.Infof("mount %q from %v: no such directory", dir, conn.RemoteAddr())
		return nfs.MountStatusErrNoEnt, nil, flavors
	case err != nil:
		nfs.Log.Errorf("mount %q from %v: %v", dir, conn.RemoteAddr(), err)
		return nfs.MountStatusErrIO, nil, flavors
	case !st.IsDir():
		return nfs.MountStatusErrNotDir, nil, flavors
	}
	view, err := h.root.Chroot(dir)
	if err != nil {
		return nfs.MountStatusErrServerFault, nil, flavors
	}
	nfs.Log.Infof("mount %q from %v", dir, conn.RemoteAddr())
	return nfs.MountStatusOk, view, flavors
}

func (h *Handler) Change(f billy.Filesystem) billy.Change {
	if c, ok := f.(billy.Change); ok {
		return c
	}
	return nil
}

// FSStat reports the free space of the disk holding the database, so df and
// Finder's free-space check see real numbers.
func (h *Handler) FSStat(_ context.Context, _ billy.Filesystem, s *nfs.FSStat) error {
	total, free, err := diskStats(h.root.DB().Path())
	if err != nil {
		return nil // keep go-nfs's defaults rather than failing the call
	}
	s.TotalSize, s.FreeSize = total, free
	if s.AvailableSize != 0 { // go-nfs zeroes it for read-only exports
		s.AvailableSize = free
	}
	return nil
}

// absolute returns the path of p within f, expressed from the export root.
func absolute(f billy.Filesystem, p []string) []string {
	v, ok := f.(*boltfs.FS)
	if !ok || len(v.Base()) == 0 {
		return p
	}
	full := make([]string, 0, len(v.Base())+len(p))
	full = append(full, v.Base()...)
	return append(full, p...)
}

func (h *Handler) ToHandle(f billy.Filesystem, p []string) []byte {
	full := absolute(f, p)
	if joined := strings.Join(full, "/"); len(joined) <= maxPayload {
		return encodeHandle(handlePath, []byte(joined))
	}
	inner := h.inner.ToHandle(h.root, full)
	if len(inner) > maxPayload {
		nfs.Log.Errorf("handle for %q does not fit in %d bytes", strings.Join(full, "/"), maxHandle)
		return nil
	}
	return encodeHandle(handleCached, inner)
}

func (h *Handler) FromHandle(fh []byte) (billy.Filesystem, []string, error) {
	kind, payload, ok := decodeHandle(fh)
	if !ok {
		return nil, nil, &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusStale}
	}
	switch kind {
	case handlePath:
		if len(payload) == 0 {
			return h.root, []string{}, nil
		}
		return h.root, strings.Split(string(payload), "/"), nil
	case handleCached:
		return h.inner.FromHandle(payload)
	}
	return nil, nil, &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusStale}
}

func (h *Handler) InvalidateHandle(f billy.Filesystem, fh []byte) error {
	if kind, payload, ok := decodeHandle(fh); ok && kind == handleCached {
		return h.inner.InvalidateHandle(f, payload)
	}
	return nil
}

func (h *Handler) HandleLimit() int { return h.inner.HandleLimit() }

func (h *Handler) VerifierFor(path string, contents []fs.FileInfo) uint64 {
	return h.inner.(nfs.CachingHandler).VerifierFor(path, contents)
}

func (h *Handler) DataForVerifier(path string, verifier uint64) []fs.FileInfo {
	return h.inner.(nfs.CachingHandler).DataForVerifier(path, verifier)
}

// base is the handler wrapped by go-nfs's caching helper. Only its handle and
// verifier bookkeeping is used; everything else defers to the outer handler.
type base struct{ h *Handler }

func (b *base) Mount(ctx context.Context, c net.Conn, r nfs.MountRequest) (nfs.MountStatus, billy.Filesystem, []nfs.AuthFlavor) {
	return b.h.Mount(ctx, c, r)
}
func (b *base) Change(f billy.Filesystem) billy.Change { return b.h.Change(f) }
func (b *base) FSStat(ctx context.Context, f billy.Filesystem, s *nfs.FSStat) error {
	return b.h.FSStat(ctx, f, s)
}
func (b *base) ToHandle(billy.Filesystem, []string) []byte            { return nil }
func (b *base) FromHandle([]byte) (billy.Filesystem, []string, error) { return nil, nil, nil }
func (b *base) InvalidateHandle(billy.Filesystem, []byte) error       { return nil }
func (b *base) HandleLimit() int                                      { return -1 }
