package nfsserver

import (
	"strings"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"

	"github.com/nontechno/experimental/etcd-nfs/etcdfs"
)

// File handle format.
//
// Handles for paths up to 63 bytes encode the path itself, so they stay valid
// across server restarts and mounts do not go stale. Longer paths fall back
// to go-nfs's in-memory cache.
//
//	'P' path 0-pad         path below the top-level root
//	'H' len handle 0-pad   go-nfs caching-handler handle
//
// Handles are always padded to a multiple of 4 bytes. XDR pads opaque data
// to 4-byte boundaries, and go-nfs reads the handle of ACCESS, SETATTR,
// COMMIT, FSSTAT and others without consuming that padding, so an unaligned
// handle shifts every following field. (An ACCESS mask read as 0 makes the
// Linux client fail with "Permission denied".)
const (
	maxHandle    = 64 // NFSv3 limit
	handlePath   = 'P'
	handleCached = 'H'
)

func (h *Handler) ToHandle(f billy.Filesystem, p []string) []byte {
	full := p
	if v, ok := f.(*etcdfs.FS); ok && v.Base() != "" {
		full = append(strings.Split(v.Base(), "/"), p...)
	}
	if joined := strings.Join(full, "/"); 1+len(joined) <= maxHandle {
		return pad4(append([]byte{handlePath}, joined...))
	}
	inner := h.cache.ToHandle(h.root, full)
	return pad4(append([]byte{handleCached, byte(len(inner))}, inner...))
}

func (h *Handler) FromHandle(fh []byte) (billy.Filesystem, []string, error) {
	if len(fh) > 0 && fh[0] == handlePath {
		// Paths never contain NUL, so trailing NULs are padding.
		joined := strings.TrimRight(string(fh[1:]), "\x00")
		if joined == "" {
			return h.root, []string{}, nil
		}
		return h.root, strings.Split(joined, "/"), nil
	}
	if inner, ok := cachedHandle(fh); ok {
		return h.cache.FromHandle(inner)
	}
	return nil, nil, &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusStale}
}

func (h *Handler) InvalidateHandle(f billy.Filesystem, fh []byte) error {
	if inner, ok := cachedHandle(fh); ok {
		return h.cache.InvalidateHandle(f, inner)
	}
	return nil // path handles need no bookkeeping
}

func (h *Handler) HandleLimit() int { return h.cache.HandleLimit() }

func cachedHandle(fh []byte) ([]byte, bool) {
	if len(fh) < 2 || fh[0] != handleCached || 2+int(fh[1]) > len(fh) {
		return nil, false
	}
	return fh[2 : 2+int(fh[1])], true
}

func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}
