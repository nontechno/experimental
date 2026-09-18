package boltfs

import (
	"path"
	"strings"
)

// Mapping
//
//	bucket  -> directory
//	key     -> file name
//	value   -> file content
//
// A bbolt bucket holds keys and nested buckets in one namespace, so a name is
// either a file or a directory, never both. That matches POSIX directory
// entries exactly, which is why no marker keys or escaping are needed.
//
// The one structural mismatch: the bbolt database root can hold buckets but
// not keys. With no root bucket configured, the top level of the filesystem
// therefore accepts directories only; see Options.RootBucket.

// maxNameLen bounds a single path component. NFSv3 clients expect NAME_MAX,
// and bbolt keys must stay well under the page size.
const maxNameLen = 255

// splitPath turns a billy path into cleaned path elements relative to the
// filesystem root. The root itself yields an empty slice.
func splitPath(p string) []string {
	p = path.Clean("/" + strings.ReplaceAll(p, `\`, "/"))
	if p == "/" {
		return nil
	}
	return strings.Split(p[1:], "/")
}

// joinPath is the inverse of splitPath.
func joinPath(elems []string) string { return strings.Join(elems, "/") }

// validName reports whether name can be used as a directory entry. bbolt keys
// are arbitrary byte strings, so a pre-existing database may hold names that
// no filesystem can express; those are skipped when listing and refused when
// creating, rather than silently mangled.
func validName(name string) bool {
	switch {
	case name == "", name == ".", name == "..":
		return false
	case len(name) > maxNameLen:
		return false
	case strings.ContainsAny(name, "/\x00"):
		return false
	}
	return true
}

// chain returns the full bucket path from the database root, without
// aliasing f.root.
func (f *FS) chain(elems []string) []string {
	out := make([]string, 0, len(f.root)+len(elems))
	out = append(out, f.root...)
	return append(out, elems...)
}

// absPath is the path from the database root, used for stable file ids.
func (f *FS) absPath(elems []string) string { return joinPath(f.chain(elems)) }
