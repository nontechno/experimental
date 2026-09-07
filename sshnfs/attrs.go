package main

import (
	"hash/fnv"
	"os"
	"path"
	"sync"
	"time"

	"github.com/pkg/sftp"
	nfsfile "github.com/willscott/go-nfs/file"
)

// fileInfo decorates an sftp FileInfo with the extra fields go-nfs needs
// (link count, owner, and a stable file id) via Sys().
type fileInfo struct {
	os.FileInfo
	sys *nfsfile.FileInfo
}

func (f *fileInfo) Sys() interface{} { return f.sys }

// inodeFor derives a stable, non-zero file id from the remote path. SFTP does
// not expose inode numbers, and NFS clients dislike a fileid of 0.
func inodeFor(remotePath string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(remotePath))
	id := h.Sum64()
	if id == 0 {
		id = 1
	}
	return id
}

// wrap converts an os.FileInfo obtained from sftp into one carrying ownership
// information, applying the configured uid/gid squashing.
func (f *FS) wrap(remotePath string, fi os.FileInfo) os.FileInfo {
	if fi == nil {
		return nil
	}
	var uid, gid uint32
	if st, ok := fi.Sys().(*sftp.FileStat); ok && st != nil {
		uid, gid = st.UID, st.GID
	}
	if f.uid >= 0 {
		uid = uint32(f.uid)
	}
	if f.gid >= 0 {
		gid = uint32(f.gid)
	}
	nlink := uint32(1)
	if fi.IsDir() {
		nlink = 2
	}
	return &fileInfo{
		FileInfo: fi,
		sys: &nfsfile.FileInfo{
			Nlink:  nlink,
			UID:    uid,
			GID:    gid,
			Fileid: inodeFor(remotePath),
		},
	}
}

type attrEntry struct {
	fi      os.FileInfo
	expires time.Time
}

type dirEntry struct {
	list    []os.FileInfo
	expires time.Time
}

// attrCache is a short lived cache of stat results and directory listings.
// Every round trip it saves is one avoided SSH latency hit, which is what
// makes an NFS export over SFTP usable interactively.
type attrCache struct {
	mu      sync.Mutex
	attrTTL time.Duration
	dirTTL  time.Duration
	max     int
	attrs   map[string]attrEntry
	dirs    map[string]dirEntry
}

func newAttrCache(attrTTL, dirTTL time.Duration) *attrCache {
	return &attrCache{
		attrTTL: attrTTL,
		dirTTL:  dirTTL,
		max:     8192,
		attrs:   make(map[string]attrEntry),
		dirs:    make(map[string]dirEntry),
	}
}

// attrKey separates stat results from lstat results. Sharing one key would let
// a Stat on a symlink hand the target's attributes to a later Lstat, and the
// client would never learn the entry is a link.
func attrKey(remotePath string, lstat bool) string {
	if lstat {
		return "L\x00" + remotePath
	}
	return "S\x00" + remotePath
}

func (c *attrCache) getAttr(remotePath string, lstat bool) (os.FileInfo, bool) {
	if c.attrTTL <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.attrs[attrKey(remotePath, lstat)]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.fi, true
}

func (c *attrCache) putAttr(remotePath string, lstat bool, fi os.FileInfo) {
	if c.attrTTL <= 0 || fi == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.attrs) >= c.max {
		c.sweepLocked()
	}
	c.attrs[attrKey(remotePath, lstat)] = attrEntry{fi: fi, expires: time.Now().Add(c.attrTTL)}
}

func (c *attrCache) getDir(remotePath string) ([]os.FileInfo, bool) {
	if c.dirTTL <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.dirs[remotePath]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.list, true
}

func (c *attrCache) putDir(remotePath string, list []os.FileInfo) {
	if c.dirTTL <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.dirs) >= c.max {
		c.sweepLocked()
	}
	c.dirs[remotePath] = dirEntry{list: list, expires: time.Now().Add(c.dirTTL)}
}

// invalidateAttr drops only what we know about the object itself. This is the
// right scope for a write or a truncate: the file changed, the directory it
// lives in did not, and dropping the parent listing on every WRITE would make
// a large copy re-read the whole directory thousands of times.
func (c *attrCache) invalidateAttr(remotePath string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.attrs, attrKey(remotePath, false))
	delete(c.attrs, attrKey(remotePath, true))
	delete(c.dirs, remotePath)
}

// invalidate additionally drops the containing directory's listing, for
// operations that add or remove a name.
func (c *attrCache) invalidate(remotePath string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.attrs, attrKey(remotePath, false))
	delete(c.attrs, attrKey(remotePath, true))
	delete(c.dirs, remotePath)
	delete(c.dirs, path.Dir(remotePath))
}

func (c *attrCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attrs = make(map[string]attrEntry)
	c.dirs = make(map[string]dirEntry)
}

func (c *attrCache) sweepLocked() {
	now := time.Now()
	for k, v := range c.attrs {
		if now.After(v.expires) {
			delete(c.attrs, k)
		}
	}
	for k, v := range c.dirs {
		if now.After(v.expires) {
			delete(c.dirs, k)
		}
	}
	if len(c.attrs) >= c.max {
		c.attrs = make(map[string]attrEntry)
	}
	if len(c.dirs) >= c.max {
		c.dirs = make(map[string]dirEntry)
	}
}
