package boltfs

import (
	"sync"
	"time"
)

// mtimes remembers when each path was last modified.
//
// bbolt stores no timestamps, but NFS clients (macOS especially) compare
// mtime to decide whether cached file data and directory listings are still
// valid. Since bbolt holds an exclusive file lock, this process is the only
// writer while it runs, so an in-memory table is authoritative for the
// lifetime of the server.
//
// Paths not in the table report base, set at startup. Every recorded time is
// strictly greater than base and strictly increasing, so a client can never
// mistake a changed object for an unchanged one. Restarting the server moves
// every mtime forward, which costs a round of revalidation and nothing else.
type mtimes struct {
	mu    sync.Mutex
	base  time.Time
	last  time.Time
	table map[string]time.Time
	limit int
}

func newMtimes(limit int) *mtimes {
	now := time.Now()
	return &mtimes{base: now, last: now, table: make(map[string]time.Time), limit: limit}
}

// touch records that the given paths changed. Passing several paths (an
// object and its parent directory) gives them all the same timestamp.
func (m *mtimes) touch(paths ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if !now.After(m.last) {
		// Guarantee strict monotonicity even if the wall clock stalls or
		// steps backwards.
		now = m.last.Add(time.Nanosecond)
	}
	m.last = now
	if len(m.table)+len(paths) > m.limit {
		// Forget everything rather than evict arbitrarily: entries fall back
		// to a base that is now later than any timestamp handed out before,
		// so clients revalidate instead of trusting a stale listing.
		m.table = make(map[string]time.Time, m.limit/2)
		m.base = now
	}
	for _, p := range paths {
		m.table[p] = now
	}
}

func (m *mtimes) get(path string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.table[path]; ok {
		return t
	}
	return m.base
}

// forget drops a path's entry, used when an object is removed so a later
// object with the same name does not inherit its timestamp.
func (m *mtimes) forget(prefix string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.table, prefix)
	for k := range m.table {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix && k[len(prefix)] == '/' {
			delete(m.table, k)
		}
	}
}
