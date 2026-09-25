package main

import (
	"reflect"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
)

func st(h string) *entryState {
	return &entryState{hash: plumbing.NewHash(h), mode: filemode.Regular}
}

func TestFindConflicts(t *testing.T) {
	a, b := st("aa"), st("bb")
	tests := []struct {
		name         string
		ours, theirs map[string]*entryState
		want         []string
	}{
		{"disjoint", map[string]*entryState{"x": a}, map[string]*entryState{"y": b}, []string{}},
		{"same change", map[string]*entryState{"x": a}, map[string]*entryState{"x": a}, []string{}},
		{"both deleted", map[string]*entryState{"x": nil}, map[string]*entryState{"x": nil}, []string{}},
		{"different content", map[string]*entryState{"x": a}, map[string]*entryState{"x": b}, []string{"x"}},
		{"modify vs delete", map[string]*entryState{"x": a}, map[string]*entryState{"x": nil}, []string{"x"}},
		{"file vs dir (ours file)", map[string]*entryState{"d": a}, map[string]*entryState{"d/f": b}, []string{"d"}},
		{"file vs dir (theirs file)", map[string]*entryState{"d/f": a}, map[string]*entryState{"d": b}, []string{"d"}},
		{"both delete file, theirs adds dir", map[string]*entryState{"d": nil}, map[string]*entryState{"d": nil, "d/f": b}, []string{}},
		{"ours deletes in dir, theirs replaces dir", map[string]*entryState{"d/f": nil}, map[string]*entryState{"d/f": nil, "d": b}, []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := findConflicts(tc.ours, tc.theirs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidatePath(t *testing.T) {
	for _, p := range []string{"a", "a/b.txt", ".github/workflows/x.yml", ".gitignore", "a/.gitx"} {
		if err := validatePath(p); err != nil {
			t.Errorf("%q: unexpected error %v", p, err)
		}
	}
	for _, p := range []string{"", "/etc/passwd", "../x", "a/../b", "a//b", "./a",
		".git/config", "a/.GIT/hooks/x", "a/.gitmount-tmp-1", "a\x00b"} {
		if err := validatePath(p); err == nil {
			t.Errorf("%q: expected error", p)
		}
	}
}

func TestAncestors(t *testing.T) {
	if got := ancestors("a/b/c"); !reflect.DeepEqual(got, []string{"a/b", "a"}) {
		t.Errorf("got %v", got)
	}
	if got := ancestors("a"); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}
