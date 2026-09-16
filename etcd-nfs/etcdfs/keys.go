package etcdfs

import (
	"path"
	"strings"
)

// Paths inside the package are "rel" paths: slash-separated, relative to the
// view's root, with no leading or trailing slash; "" is the root itself.

// cleanRel converts a billy path into a rel path.
func cleanRel(p string) string {
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}

// parentRel returns the parent of rel ("" for top-level entries).
func parentRel(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return ""
}

func baseName(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[i+1:]
	}
	if rel == "" {
		return "/"
	}
	return rel
}

// fileKey is the key holding the content of file rel.
func (f *FS) fileKey(rel string) string { return f.root + rel }

// dirKey is the marker key of directory rel, which is also the prefix of
// every key inside it. For the root it is the root prefix.
func (f *FS) dirKey(rel string) string {
	if rel == "" {
		return f.root
	}
	return f.root + rel + "/"
}

// absRel converts a rel path of this view into one relative to the top-level
// root, so file ids and handles do not depend on which view produced them.
func (f *FS) absRel(rel string) string {
	switch {
	case f.base == "":
		return rel
	case rel == "":
		return f.base
	default:
		return f.base + "/" + rel
	}
}

// validName reports whether a key segment can be shown as a directory entry.
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsRune(name, 0)
}
