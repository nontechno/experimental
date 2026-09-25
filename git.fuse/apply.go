package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// tmpPrefix names temp files created while writing into the work tree.
// The FUSE layer refuses to create such names, and leftovers from a crash
// are removed at startup.
const tmpPrefix = ".gitmount-tmp-"

// entryState is the state of one file path in a tree; nil means absent.
type entryState struct {
	hash plumbing.Hash
	mode filemode.FileMode
}

func (e *entryState) equal(o *entryState) bool {
	if e == nil || o == nil {
		return e == o
	}
	return e.hash == o.hash && e.mode == o.mode
}

// changedPaths returns every file path that differs between from and to
// (either may be nil), mapped to its state in to.
func changedPaths(from, to *object.Tree) (map[string]*entryState, error) {
	changes, err := object.DiffTree(from, to)
	if err != nil {
		return nil, err
	}
	out := map[string]*entryState{}
	for _, ch := range changes {
		for _, name := range []string{ch.From.Name, ch.To.Name} {
			if name == "" {
				continue
			}
			if _, done := out[name]; done {
				continue
			}
			st, err := lookup(to, name)
			if err != nil {
				return nil, err
			}
			out[name] = st
		}
	}
	return out, nil
}

// lookup returns the state of file path p in t (nil if absent or a
// directory).
func lookup(t *object.Tree, p string) (*entryState, error) {
	e, err := lookupEntry(t, p)
	if err != nil || e == nil || e.Mode == filemode.Dir {
		return nil, err
	}
	return &entryState{hash: e.Hash, mode: e.Mode}, nil
}

// lookupEntry finds p in t. It walks the tree itself rather than using
// Tree.FindEntry, which fails with "object not found" when a parent
// component is a file in t (e.g. a directory replaced by a file).
func lookupEntry(t *object.Tree, p string) (*object.TreeEntry, error) {
	if t == nil {
		return nil, nil
	}
	parts := strings.Split(p, "/")
	cur := t
	for i, name := range parts {
		var e *object.TreeEntry
		for j := range cur.Entries {
			if cur.Entries[j].Name == name {
				e = &cur.Entries[j]
				break
			}
		}
		if e == nil {
			return nil, nil
		}
		if i == len(parts)-1 {
			return e, nil
		}
		if e.Mode != filemode.Dir {
			return nil, nil // a parent component is a file
		}
		sub, err := cur.Tree(name)
		if err != nil {
			return nil, err
		}
		cur = sub
	}
	return nil, nil
}

func ancestors(p string) []string {
	var out []string
	for d := path.Dir(p); d != "."; d = path.Dir(d) {
		out = append(out, d)
	}
	return out
}

// findConflicts reports paths changed differently on both sides, including
// file/directory clashes (one side changes "a", the other "a/b").
func findConflicts(ours, theirs map[string]*entryState) []string {
	set := map[string]bool{}
	for p, t := range theirs {
		if o, ok := ours[p]; ok && !o.equal(t) {
			set[p] = true
		}
		for _, a := range ancestors(p) {
			if o, ok := ours[a]; ok && o != nil {
				set[a] = true
			}
		}
	}
	for p, o := range ours {
		if o == nil {
			continue
		}
		for _, a := range ancestors(p) {
			if t, ok := theirs[a]; ok && t != nil {
				set[a] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// validatePath rejects paths git itself would refuse to check out.
func validatePath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return fmt.Errorf("invalid path %q in repository", p)
	}
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." || c == ".." || isDotGit(c) || strings.HasPrefix(c, tmpPrefix) {
			return fmt.Errorf("refusing unsafe path %q in repository", p)
		}
	}
	return nil
}

// apply makes the work tree match the given file states. head is the tree
// the work tree currently matches (nil if empty).
//
// Everything that could make it fail part-way is checked before the first
// change: unsafe or unsupported entries, and local untracked or ignored
// files that would be overwritten or would block replacing a directory.
// All writes go through an os.Root, so nothing is ever written outside the
// work tree or through a symlink.
func (r *Repo) apply(changes map[string]*entryState, head *object.Tree) error {
	var dels, writes []string
	for p, st := range changes {
		if err := validatePath(p); err != nil {
			return err
		}
		switch {
		case st == nil:
			dels = append(dels, p)
		case st.mode == filemode.Submodule:
			return fmt.Errorf("%s: git submodules are not supported", p)
		case st.mode == filemode.Regular, st.mode == filemode.Deprecated,
			st.mode == filemode.Executable, st.mode == filemode.Symlink:
			writes = append(writes, p)
		default:
			return fmt.Errorf("%s: unsupported file mode %v", p, st.mode)
		}
	}
	// Deepest first, so emptied directories can be pruned; then create
	// in lexical order, so parents come before children.
	sort.Sort(sort.Reverse(sort.StringSlice(dels)))
	sort.Strings(writes)

	if err := r.checkClobber(writes, dels, head); err != nil {
		return err
	}

	for _, p := range dels {
		if err := r.checkParents(p); err != nil {
			return err
		}
		if err := r.root.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		r.pruneEmptyParents(p)
	}
	for _, p := range writes {
		if err := r.writeEntry(p, changes[p]); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

// checkClobber refuses to apply when a write would overwrite a local file
// git does not track (untracked or ignored), or would replace a directory
// that still holds such files after the deletions.
func (r *Repo) checkClobber(writes, dels []string, head *object.Tree) error {
	deleted := map[string]bool{}
	for _, p := range dels {
		deleted[p] = true
	}
	var bad []string
	for _, p := range writes {
		fi, err := r.root.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if fi.IsDir() {
			// Must become empty through the deletions.
			err := fs.WalkDir(r.root.FS(), p, func(q string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() && !deleted[q] {
					bad = append(bad, q)
				}
				return nil
			})
			if err != nil {
				return err
			}
			continue
		}
		old, err := lookupEntry(head, p)
		if err != nil {
			return err
		}
		if old == nil {
			bad = append(bad, p) // exists locally but is not tracked
		}
	}
	if len(bad) > 0 {
		return &ConflictError{Reason: "upstream changes would overwrite local untracked or ignored files", Paths: bad}
	}
	return nil
}

// checkParents ensures every existing parent component is a real directory.
func (r *Repo) checkParents(p string) error {
	anc := ancestors(p)
	for i := len(anc) - 1; i >= 0; i-- {
		fi, err := r.root.Lstat(anc[i])
		if errors.Is(err, fs.ErrNotExist) {
			return nil // the rest will be created
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s: parent %s is not a directory", p, anc[i])
		}
	}
	return nil
}

func (r *Repo) pruneEmptyParents(p string) {
	for _, d := range ancestors(p) {
		fi, err := r.root.Lstat(d)
		if err != nil || !fi.IsDir() {
			return
		}
		if r.root.Remove(d) != nil {
			return // not empty
		}
	}
}

func (r *Repo) writeEntry(p string, st *entryState) error {
	perm := os.FileMode(0o644)
	if st.mode == filemode.Executable {
		perm = 0o755
	}
	if err := r.checkParents(p); err != nil {
		return err
	}
	if d := path.Dir(p); d != "." {
		if err := r.root.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	blob, err := r.r.BlobObject(st.hash)
	if err != nil {
		return err
	}
	rd, err := blob.Reader()
	if err != nil {
		return err
	}
	defer rd.Close()

	var suffix [8]byte
	rand.Read(suffix[:])
	tmp := path.Join(path.Dir(p), tmpPrefix+hex.EncodeToString(suffix[:]))

	if st.mode == filemode.Symlink {
		target, err := io.ReadAll(io.LimitReader(rd, 4096))
		if err != nil {
			return err
		}
		if err := r.root.Symlink(string(target), tmp); err != nil {
			return err
		}
	} else {
		f, err := r.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, rd)
		if err == nil {
			err = f.Chmod(perm) // independent of umask
		}
		if err == nil {
			// Durable before it becomes visible: after a crash the
			// recovery commit must never record a truncated file.
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			r.root.Remove(tmp)
			return err
		}
	}
	// Atomic replace: readers see either the old or the new content.
	if err := r.root.Rename(tmp, p); err != nil {
		r.root.Remove(tmp)
		return err
	}
	return nil
}
