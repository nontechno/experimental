package dbfs

import (
	"sync"
	"time"
)

// cache is a small TTL cache with single-flight loading and a total size
// budget. NFS issues many GETATTR/LOOKUP/READ calls for the same path, and
// each of them must see the same bytes and size, so every generated value
// goes through here.
type cache struct {
	mu      sync.Mutex
	now     func() time.Time
	budget  int64
	used    int64
	entries map[string]*cacheEntry
}

type cacheEntry struct {
	ready   chan struct{} // closed when loading finishes
	value   any
	size    int64
	err     error
	counted bool // size is included in cache.used
	loaded  time.Time
	expires time.Time
}

func newCache(budget int64) *cache {
	return &cache{now: time.Now, budget: budget, entries: map[string]*cacheEntry{}}
}

// get returns the cached value for key, calling load at most once
// concurrently per key. load returns the value, its approximate size in
// bytes, how long it may be cached, and an error (errors are not cached).
func (c *cache) get(key string, load func() (any, int64, time.Duration, error)) (value any, loaded time.Time, err error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		select {
		case <-e.ready:
			if c.now().Before(e.expires) {
				c.mu.Unlock()
				return e.value, e.loaded, nil
			}
			c.removeLocked(key, e)
		default:
			c.mu.Unlock()
			<-e.ready
			if e.err != nil {
				return nil, time.Time{}, e.err
			}
			return e.value, e.loaded, nil
		}
	}
	e := &cacheEntry{ready: make(chan struct{})}
	c.entries[key] = e
	c.mu.Unlock()

	v, size, ttl, err := load()

	c.mu.Lock()
	e.value, e.size, e.err = v, size, err
	e.loaded = c.now()
	e.expires = e.loaded.Add(ttl)
	close(e.ready)
	if err != nil || ttl <= 0 {
		c.removeLocked(key, e)
	} else {
		e.counted = true
		c.used += size
		c.evictLocked(key)
	}
	c.mu.Unlock()
	return v, e.loaded, err
}

func (c *cache) removeLocked(key string, e *cacheEntry) {
	if c.entries[key] == e {
		delete(c.entries, key)
		if e.counted {
			c.used -= e.size
			e.counted = false
		}
	}
}

// evictLocked drops expired entries, then the oldest ones, until the cache
// fits its budget. The entry just stored (keep) is never evicted, so a
// single oversized value is still served once.
func (c *cache) evictLocked(keep string) {
	if c.used <= c.budget {
		return
	}
	now := c.now()
	for k, e := range c.entries {
		if k != keep && isReady(e) && !now.Before(e.expires) {
			c.removeLocked(k, e)
		}
	}
	for c.used > c.budget {
		var oldestKey string
		var oldest *cacheEntry
		for k, e := range c.entries {
			if k == keep || !isReady(e) {
				continue
			}
			if oldest == nil || e.loaded.Before(oldest.loaded) {
				oldestKey, oldest = k, e
			}
		}
		if oldest == nil {
			return
		}
		c.removeLocked(oldestKey, oldest)
	}
}

func isReady(e *cacheEntry) bool {
	select {
	case <-e.ready:
		return true
	default:
		return false
	}
}
