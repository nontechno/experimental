package main

import (
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Gate coordinates FUSE operations with repository operations.
//
//   - Every mutating FUSE operation runs under a shared (read) lock and marks
//     the tree dirty. Repository operations that read or rewrite the work
//     tree (commit, integrate) run under the exclusive lock, so they never
//     observe a half-applied operation and never race with one.
//   - Writable file handles are counted. The work tree is only rewritten by
//     a pull when no writable handle is open: pulled files replace the old
//     ones by rename, and writes through an already-open handle would
//     otherwise land in an orphaned inode and be lost.
type Gate struct {
	mu         sync.RWMutex
	writers    atomic.Int64
	dirtySince atomic.Int64 // unix nanos of first unrecorded change; 0 = clean
	lastChange atomic.Int64 // unix nanos of most recent change
}

// Mutate runs fn under the shared lock and marks the tree dirty. The tree is
// marked dirty even if fn fails, since a failed operation may still have
// had partial effects; an empty commit attempt is harmless.
func (g *Gate) Mutate(fn func() syscall.Errno) syscall.Errno {
	g.mu.RLock()
	defer g.mu.RUnlock()
	errno := fn()
	g.markDirtyLocked()
	return errno
}

func (g *Gate) markDirtyLocked() {
	now := time.Now().UnixNano()
	g.lastChange.Store(now)
	g.dirtySince.CompareAndSwap(0, now)
}

// MarkDirty flags the tree dirty from outside FUSE (used at startup to
// pick up changes left uncommitted by a crash).
func (g *Gate) MarkDirty() {
	g.mu.RLock()
	defer g.mu.RUnlock()
	g.markDirtyLocked()
}

func (g *Gate) Dirty() bool { return g.dirtySince.Load() != 0 }

func (g *Gate) DirtySince() time.Time { return time.Unix(0, g.dirtySince.Load()) }

func (g *Gate) LastChange() time.Time { return time.Unix(0, g.lastChange.Load()) }

func (g *Gate) Writers() int64 { return g.writers.Load() }

func (g *Gate) addWriter()  { g.writers.Add(1) }
func (g *Gate) dropWriter() { g.writers.Add(-1) }

// Exclusive runs fn with all FUSE mutations blocked.
func (g *Gate) Exclusive(fn func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return fn()
}

// takeDirtyLocked clears the dirty flag and returns whether it was set.
// Must be called under the exclusive lock, so no mutation can race with it:
// any change made after the lock is released will set it again.
func (g *Gate) takeDirtyLocked() bool {
	return g.dirtySince.Swap(0) != 0
}
