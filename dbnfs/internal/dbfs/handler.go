package dbfs

import (
	"context"
	"crypto/sha256"
	"net"
	"strings"
	"sync"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
)

// Handler implements nfs.Handler for a single read-only FS.
//
// File handles are derived from the path (SHA-256, truncated to 16 bytes),
// so the same path always gets the same handle. helpers.CachingHandler is
// not used because it hands out random handles from a bounded LRU: browsing
// a large directory evicts handles that clients still hold (ESTALE), and its
// FromHandle scans the whole cache on every request.
//
// The map only grows with paths that were successfully looked up, so it is
// bounded by the size of the exposed tree. After a server restart clients
// get ESTALE for handles other than the root and must look paths up again.
type Handler struct {
	fs billy.Filesystem

	mu    sync.RWMutex
	paths map[[handleLen]byte][]string
}

const handleLen = 16

var _ nfs.Handler = (*Handler)(nil)

// NewHandler returns a handler serving fs to every mount request.
func NewHandler(fs billy.Filesystem) *Handler {
	h := &Handler{fs: fs, paths: map[[handleLen]byte][]string{}}
	h.ToHandle(fs, nil) // the root handle is always resolvable
	return h
}

// Mount accepts any export path and exposes the whole tree. Access control
// is the listener address (see config nfs.listen).
func (h *Handler) Mount(_ context.Context, _ net.Conn, _ nfs.MountRequest) (nfs.MountStatus, billy.Filesystem, []nfs.AuthFlavor) {
	return nfs.MountStatusOk, h.fs, []nfs.AuthFlavor{nfs.AuthFlavorNull}
}

// Change returns nil: attributes cannot be changed.
func (h *Handler) Change(billy.Filesystem) billy.Change { return nil }

// FSStat reports an empty, full filesystem.
func (h *Handler) FSStat(context.Context, billy.Filesystem, *nfs.FSStat) error { return nil }

func handleFor(path []string) [handleLen]byte {
	sum := sha256.Sum256([]byte("dbnfs\x00" + strings.Join(path, "\x00")))
	var k [handleLen]byte
	copy(k[:], sum[:handleLen])
	return k
}

// ToHandle returns the stable handle for path.
func (h *Handler) ToHandle(_ billy.Filesystem, path []string) []byte {
	k := handleFor(path)
	h.mu.RLock()
	_, known := h.paths[k]
	h.mu.RUnlock()
	if !known {
		cp := append([]string(nil), path...)
		h.mu.Lock()
		h.paths[k] = cp
		h.mu.Unlock()
	}
	return append([]byte(nil), k[:]...)
}

// FromHandle resolves a handle produced by ToHandle.
func (h *Handler) FromHandle(fh []byte) (billy.Filesystem, []string, error) {
	if len(fh) != handleLen {
		return nil, nil, &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusStale}
	}
	var k [handleLen]byte
	copy(k[:], fh)
	h.mu.RLock()
	p, ok := h.paths[k]
	h.mu.RUnlock()
	if !ok {
		return nil, nil, &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusStale}
	}
	return h.fs, append([]string(nil), p...), nil
}

// InvalidateHandle is a no-op: nothing is ever deleted.
func (h *Handler) InvalidateHandle(billy.Filesystem, []byte) error { return nil }

// HandleLimit is used by go-nfs to size READDIRPLUS replies (limit/2 entries).
func (h *Handler) HandleLimit() int { return 1 << 20 }
