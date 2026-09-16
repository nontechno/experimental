package etcdfs

import (
	"sort"
	"sync"
	"time"
)

// revClock maps etcd revisions to wall-clock timestamps.
//
// etcd stores no timestamps, but NFS clients use mtime/ctime to decide when
// cached data and directory listings are stale. Every etcd response carries
// the store revision; the clock records the local time at which each new
// revision was first observed, and a key's mtime is the time of its
// ModRevision.
//
//   - A key that does not change keeps the same mtime.
//   - Every modification yields a strictly later mtime: a revision can only
//     be mapped after a response at or beyond it was observed, and recorded
//     times are strictly increasing.
//   - Keys written before the server started map to the start time; after a
//     restart all times shift, which only makes clients revalidate.
type revClock struct {
	mu    sync.Mutex
	revs  []int64
	times []time.Time
	limit int
}

func newRevClock(limit int) *revClock {
	return &revClock{limit: max(limit, 2)}
}

// observe records that the store has reached revision rev.
func (c *revClock) observe(rev int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.observeLocked(rev)
}

func (c *revClock) observeLocked(rev int64) {
	n := len(c.revs)
	if n > 0 && rev <= c.revs[n-1] {
		return
	}
	t := time.Now()
	if n > 0 && !t.After(c.times[n-1]) {
		t = c.times[n-1].Add(time.Nanosecond)
	}
	c.revs = append(c.revs, rev)
	c.times = append(c.times, t)
	if len(c.revs) > c.limit {
		// Drop the oldest half. Those revisions now map to a later time,
		// which at worst forces a client cache revalidation.
		half := len(c.revs) / 2
		c.revs = append([]int64(nil), c.revs[half:]...)
		c.times = append([]time.Time(nil), c.times[half:]...)
	}
}

// timeOf returns the timestamp of revision rev, read in a response at
// headerRev (observed first, so rev is always covered).
func (c *revClock) timeOf(rev, headerRev int64) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.observeLocked(max(rev, headerRev))
	i := sort.Search(len(c.revs), func(i int) bool { return c.revs[i] >= rev })
	return c.times[i]
}
