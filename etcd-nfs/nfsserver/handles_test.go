package nfsserver

import (
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5"

	"github.com/nontechno/experimental/etcd-nfs/etcdfs"

	nfsc "github.com/willscott/go-nfs-client/nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
)

// TestHandleAlignment is a regression test: file handles must be XDR-aligned. go-nfs reads the handle
// of ACCESS/SETATTR/COMMIT/... without consuming opaque padding, so an
// unaligned handle corrupts the following fields. Linux saw ACCESS grant 0
// and reported "Permission denied" on cd/ls.
func TestHandleAlignment(t *testing.T) {
	e := newEnv(t)
	c, err := rpc.DialTCP("tcp", e.addr, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var m nfsc.Mount
	m.Client = c
	target, err := m.Mount("/", rpc.NewAuthUnix("linux", 0, 0).Auth()) // like the Linux client
	if err != nil {
		t.Fatal(err)
	}

	// Names of every length mod 4, plus one path past the 63-byte limit.
	paths := []string{"/", "/a", "/ab", "/abc", "/abcd", "/abcde", "/" + strings.Repeat("x", 70)}
	const linuxDirMask = 0x1f // READ|LOOKUP|MODIFY|EXTEND|DELETE

	for _, p := range paths {
		if p != "/" {
			f, err := target.OpenFile(p, 0o644)
			if err != nil {
				t.Fatalf("%s: create: %v", p, err)
			}
			if _, err := f.Write([]byte("0123456789")); err != nil {
				t.Fatalf("%s: write: %v", p, err)
			}
			f.Close()
		}
		_, fh, err := target.Lookup(p, false)
		if err != nil {
			t.Fatalf("%s: lookup: %v", p, err)
		}
		if len(fh)%4 != 0 || len(fh) > 64 {
			t.Fatalf("%s: handle length %d is not XDR-aligned or too long", p, len(fh))
		}
		granted, err := target.Access(p, linuxDirMask)
		if err != nil {
			t.Fatalf("%s: access: %v", p, err)
		}
		if granted != linuxDirMask {
			t.Fatalf("%s: ACCESS granted %#x, want %#x", p, granted, linuxDirMask)
		}
		if p == "/" {
			continue
		}
		// SETATTR also reads its handle unpadded: truncate must land.
		if err := target.Setattr(p, nfsc.Sattr3{Size: nfsc.SetSize{SetIt: true, Size: 3}}); err != nil {
			t.Fatalf("%s: setattr: %v", p, err)
		}
		if v, _ := e.value(t, p[1:]); v != "012" {
			t.Fatalf("%s: after truncate got %q", p, v)
		}
	}
}

// TestHandlesSurviveRestart checks that path handles decode in a fresh
// handler (a restarted server) and are independent of the mount view.
func TestHandlesSurviveRestart(t *testing.T) {
	e := newEnv(t)
	h1, h2 := New(e.fs, Options{}), New(e.fs, Options{})
	view, _ := e.fs.Chroot("app/config")

	cases := []struct {
		fs    billy.Filesystem
		parts []string
		want  string
	}{
		{e.fs, []string{}, ""},
		{e.fs, []string{"a"}, "a"},
		{e.fs, []string{"ab"}, "ab"},
		{view, []string{}, "app/config"},
		{view, []string{"db"}, "app/config/db"},
	}
	for _, c := range cases {
		fh := h1.ToHandle(c.fs, c.parts)
		if len(fh)%4 != 0 {
			t.Fatalf("%v: handle length %d", c.parts, len(fh))
		}
		got, p, err := h2.FromHandle(fh)
		if err != nil || got.(*etcdfs.FS) != e.fs || strings.Join(p, "/") != c.want {
			t.Fatalf("%v: decoded %v %v %v, want %q", c.parts, got, p, err, c.want)
		}
	}
	if _, _, err := h2.FromHandle([]byte("Xjunk")); err == nil {
		t.Fatal("garbage handle decoded")
	}
}
