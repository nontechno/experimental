package nfsserver_test

import (
	"net"
	"path/filepath"
	"strings"
	"testing"

	nfs "github.com/willscott/go-nfs"
	nfsc "github.com/willscott/go-nfs-client/nfs"
	rpc "github.com/willscott/go-nfs-client/nfs/rpc"
	bolt "go.etcd.io/bbolt"

	"github.com/sport/bbolt-nfs/boltfs"
	"github.com/sport/bbolt-nfs/nfsserver"
)

// An NFS file handle is an XDR opaque, and the decoder go-nfs uses does not
// skip the padding that follows a length that is not a multiple of 4. Every
// field after the handle in the same request would then be misread: ACCESS
// returns a garbage mask, the kernel concludes the caller may do nothing, and
// "cd" into the mount fails with permission denied even though "ls -ld"
// works. Handles must therefore always be a multiple of 4 bytes long.
func TestHandlesAreFourByteAligned(t *testing.T) {
	db, _ := bolt.Open(filepath.Join(t.TempDir(), "h.db"), 0o600, nil)
	defer db.Close()
	fsys, err := boltfs.New(db, boltfs.Options{RootBucket: "root"})
	if err != nil {
		t.Fatal(err)
	}
	h := nfsserver.New(fsys, nfsserver.Options{})

	deep := []string{strings.Repeat("a", 60), strings.Repeat("b", 60)} // forces the cached form
	for _, p := range [][]string{
		{}, {"a"}, {"ab"}, {"abc"}, {"abcd"}, {"dir", "file.txt"},
		{strings.Repeat("x", 61)}, {strings.Repeat("x", 62)}, deep,
	} {
		fh := h.ToHandle(fsys, p)
		name := "/" + strings.Join(p, "/")
		if len(fh) == 0 || len(fh) > 64 {
			t.Fatalf("%s: handle length %d", name, len(fh))
		}
		if len(fh)%4 != 0 {
			t.Fatalf("%s: handle length %d is not a multiple of 4", name, len(fh))
		}
		_, got, err := h.FromHandle(fh)
		if err != nil {
			t.Fatalf("%s: round trip: %v", name, err)
		}
		if strings.Join(got, "/") != strings.Join(p, "/") {
			t.Fatalf("%s: round tripped to %v", name, got)
		}
	}

	for _, bad := range [][]byte{nil, {}, {'P'}, {'P', 0, 0}, {'P', 9, 'a', 0}, {'?', 0, 0, 0}} {
		if _, _, err := h.FromHandle(bad); err == nil {
			t.Fatalf("malformed handle %v accepted", bad)
		}
	}
}

// The mask a client asks for must come back intact; a zero mask is what the
// kernel reads as "no permissions".
func TestAccessMaskSurvivesTheHandle(t *testing.T) {
	db, _ := bolt.Open(filepath.Join(t.TempDir(), "acc.db"), 0o600, nil)
	defer db.Close()
	fsys, err := boltfs.New(db, boltfs.Options{RootBucket: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fsys.MkdirAll("/sub", 0o755); err != nil {
		t.Fatal(err)
	}
	fh, _ := fsys.Create("/sub/f")
	fh.Close()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() { _ = nfs.Serve(ln, nfsserver.New(fsys, nfsserver.Options{})) }()

	c, err := rpc.DialTCP("tcp", ln.Addr().String(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var m nfsc.Mount
	m.Client = c
	// Linux mounts with sec=sys, so exercise AUTH_UNIX rather than AUTH_NULL.
	target, err := m.Mount("/", rpc.NewAuthUnix("client", 0, 0).Auth())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Unmount()

	const want = 0x3f // read, lookup, modify, extend, delete, execute
	for _, p := range []string{"/", "/sub", "/sub/f"} {
		got, err := target.Access(p, want)
		if err != nil {
			t.Fatalf("access %s: %v", p, err)
		}
		if got != want {
			t.Fatalf("access %s: mask %#x, want %#x", p, got, want)
		}
	}
}
