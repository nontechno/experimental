package nfsserver_test

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"testing"

	nfs "github.com/willscott/go-nfs"
	nfsc "github.com/willscott/go-nfs-client/nfs"
	rpc "github.com/willscott/go-nfs-client/nfs/rpc"
	bolt "go.etcd.io/bbolt"

	"github.com/sport/bbolt-nfs/boltfs"
	"github.com/sport/bbolt-nfs/nfsserver"
)

// startServer serves a fresh database over NFS on a loopback port and returns
// a mounted client target alongside the filesystem.
func startServer(t *testing.T) (*nfsc.Target, *boltfs.FS) {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "e2e.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	fsys, err := boltfs.New(db, boltfs.Options{RootBucket: "root"})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() { _ = nfs.Serve(ln, nfsserver.New(fsys, nfsserver.Options{})) }()

	c, err := rpc.DialTCP(ln.Addr().Network(), ln.Addr().String(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	var mounter nfsc.Mount
	mounter.Client = c
	target, err := mounter.Mount("/", rpc.AuthNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mounter.Unmount() })
	return target, fsys
}

func TestNFSRoundTrip(t *testing.T) {
	target, fsys := startServer(t)

	if _, err := target.FSInfo(); err != nil {
		t.Fatal(err)
	}

	if _, err := target.Mkdir("/config", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Create("/config/app.yaml", 0o644); err != nil {
		t.Fatal(err)
	}

	payload := bytes.Repeat([]byte("bbolt over nfs\n"), 900) // spans several RPCs
	w, err := target.OpenFile("/config/app.yaml", 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	w.Close()

	// Read it back over the wire.
	r, err := target.Open("/config/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("read %d bytes, wrote %d", len(got), len(payload))
	}

	// And directly out of the database, to prove the value is the content.
	if err := fsys.DB().View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte("root")).Bucket([]byte("config")).Get([]byte("app.yaml"))
		if !bytes.Equal(v, payload) {
			return errors.New("stored value does not match what was written")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A key written behind the server's back is visible immediately.
	if err := fsys.DB().Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("root")).Bucket([]byte("config")).Put([]byte("extra.txt"), []byte("hi"))
	}); err != nil {
		t.Fatal(err)
	}
	ents, err := target.ReadDirPlus("/config")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		if e.Name() != "." && e.Name() != ".." {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "app.yaml" || names[1] != "extra.txt" {
		t.Fatalf("readdirplus = %v", names)
	}

	// Attributes.
	fi, _, err := target.Lookup("/config/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != int64(len(payload)) || fi.IsDir() {
		t.Fatalf("attributes = size %d dir %v", fi.Size(), fi.IsDir())
	}

	// Rename, then remove.
	if err := target.Rename("/config/extra.txt", "/config/renamed.txt"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := target.Lookup("/config/renamed.txt"); err != nil {
		t.Fatal(err)
	}
	if err := target.Remove("/config/renamed.txt"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := target.Lookup("/config/renamed.txt"); err == nil {
		t.Fatal("removed file still resolves")
	}

	// A non-empty directory cannot be removed.
	if err := target.RmDir("/config"); err == nil {
		t.Fatal("removed a non-empty directory")
	}

	// SETATTR size, the path editors use to truncate.
	if err := target.Setattr("/config/app.yaml", nfsc.Sattr3{Size: nfsc.SetSize{SetIt: true, Size: 5}}); err != nil {
		t.Fatal(err)
	}
	if fi, _, err := target.Lookup("/config/app.yaml"); err != nil {
		t.Fatal(err)
	} else if fi.Size() != 5 {
		t.Fatalf("size after truncate = %d", fi.Size())
	}
}

// Handles encode the path, so a client can keep using them after the server
// process restarts against the same database.
func TestHandlesSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "restart.db")

	serve := func() (*nfsc.Target, func()) {
		db, err := bolt.Open(path, 0o600, nil)
		if err != nil {
			t.Fatal(err)
		}
		fsys, err := boltfs.New(db, boltfs.Options{RootBucket: "root"})
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = nfs.Serve(ln, nfsserver.New(fsys, nfsserver.Options{})) }()
		c, err := rpc.DialTCP(ln.Addr().Network(), ln.Addr().String(), false)
		if err != nil {
			t.Fatal(err)
		}
		var m nfsc.Mount
		m.Client = c
		target, err := m.Mount("/", rpc.AuthNull)
		if err != nil {
			t.Fatal(err)
		}
		return target, func() {
			_ = m.Unmount()
			c.Close()
			ln.Close()
			db.Close()
		}
	}

	target, stop := serve()
	if _, err := target.Create("/keep.txt", 0o644); err != nil {
		t.Fatal(err)
	}
	_, handle, err := target.Lookup("/keep.txt")
	if err != nil {
		t.Fatal(err)
	}
	stop()

	target2, stop2 := serve()
	defer stop2()
	if _, err := target2.GetAttr(handle); err != nil {
		t.Fatalf("handle from the previous process went stale: %v", err)
	}
}

func TestMountSubdirectory(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "sub.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fsys, err := boltfs.New(db, boltfs.Options{RootBucket: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fsys.MkdirAll("/deep/inner", 0o755); err != nil {
		t.Fatal(err)
	}
	fh, err := fsys.Create("/deep/inner/note")
	if err != nil {
		t.Fatal(err)
	}
	fh.Write([]byte("found"))
	fh.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { _ = nfs.Serve(ln, nfsserver.New(fsys, nfsserver.Options{})) }()

	c, err := rpc.DialTCP(ln.Addr().Network(), ln.Addr().String(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var m nfsc.Mount
	m.Client = c
	target, err := m.Mount("/deep", rpc.AuthNull)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Unmount()

	r, err := target.Open("/inner/note")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if string(b) != "found" {
		t.Fatalf("content = %q", b)
	}

	// Writes through the sub-mount land in the right bucket.
	if _, err := target.Create("/inner/added", 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Stat("/deep/inner/added"); err != nil {
		t.Fatalf("file created through sub-mount: %v", err)
	}

	var mounter nfsc.Mount
	mounter.Client = c
	if _, err := mounter.Mount("/nonexistent", rpc.AuthNull); err == nil {
		t.Fatal("mounted a path that does not exist")
	}
}

func TestReadOnlyExportRejectsWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ro.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := boltfs.New(db, boltfs.Options{RootBucket: "root"})
	if err != nil {
		t.Fatal(err)
	}
	fh, _ := fsys.Create("/f")
	fh.Write([]byte("data"))
	fh.Close()
	db.Close()

	rodb, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer rodb.Close()
	rofs, err := boltfs.New(rodb, boltfs.Options{RootBucket: "root", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() { _ = nfs.Serve(ln, nfsserver.New(rofs, nfsserver.Options{})) }()

	c, err := rpc.DialTCP(ln.Addr().Network(), ln.Addr().String(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var m nfsc.Mount
	m.Client = c
	target, err := m.Mount("/", rpc.AuthNull)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Unmount()

	r, err := target.Open("/f")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	r.Close()
	if string(b) != "data" {
		t.Fatalf("read = %q", b)
	}
	if _, err := target.Create("/new", 0o644); err == nil {
		t.Fatal("created a file on a read-only export")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
