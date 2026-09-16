package nfsserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	nfs "github.com/willscott/go-nfs"
	nfsc "github.com/willscott/go-nfs-client/nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/nontechno/experimental/etcd-nfs/etcdfs"
)

// End-to-end tests speak the NFSv3 wire protocol to the server with
// go-nfs-client. They need ETCD_ENDPOINTS.

type env struct {
	cli    *clientv3.Client
	fs     *etcdfs.FS
	prefix string
	addr   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	eps := os.Getenv("ETCD_ENDPOINTS")
	if eps == "" {
		t.Skip("ETCD_ENDPOINTS not set")
	}
	cli, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(eps, ","), DialTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	prefix := "/etcdnfs-e2e/" + t.Name() + "-" + hex.EncodeToString(b[:]) + "/"
	fsys, err := etcdfs.New(cli, etcdfs.Options{Prefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = nfs.Serve(ln, New(fsys, Options{})) }()
	t.Cleanup(func() {
		ln.Close()
		_, _ = cli.Delete(context.Background(), prefix, clientv3.WithPrefix())
		cli.Close()
	})
	return &env{cli: cli, fs: fsys, prefix: prefix, addr: ln.Addr().String()}
}

func (e *env) mount(t *testing.T, dir string) (*nfsc.Target, error) {
	t.Helper()
	c, err := rpc.DialTCP("tcp", e.addr, false)
	if err != nil {
		t.Fatal(err)
	}
	var m nfsc.Mount
	m.Client = c
	target, err := m.Mount(dir, rpc.AuthNull)
	if err != nil {
		c.Close()
		return nil, err
	}
	t.Cleanup(func() { _ = m.Unmount(); c.Close() })
	return target, nil
}

func (e *env) value(t *testing.T, key string) (string, bool) {
	t.Helper()
	r, err := e.cli.Get(context.Background(), e.prefix+key)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Kvs) == 0 {
		return "", false
	}
	return string(r.Kvs[0].Value), true
}

func TestE2EFileLifecycle(t *testing.T) {
	e := newEnv(t)
	target, err := e.mount(t, "/")
	if err != nil {
		t.Fatal(err)
	}

	// Large enough to span several WRITE RPCs.
	payload := bytes.Repeat([]byte("0123456789abcdef"), 20000) // 320 KiB
	f, err := target.OpenFile("/data.bin", 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(payload); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if v, _ := e.value(t, "data.bin"); v != string(payload) {
		t.Fatalf("etcd value mismatch: %d bytes", len(v))
	}

	r, err := target.Open("/data.bin")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("read back %d bytes, want %d", len(got), len(payload))
	}

	// Keys written directly to etcd are visible over NFS.
	if _, err := e.cli.Put(context.Background(), e.prefix+"app/config/db", "host=x"); err != nil {
		t.Fatal(err)
	}
	entries, err := target.ReadDirPlus("/")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, ent := range entries {
		seen[ent.FileName] = true
		if ent.FileName == "data.bin" && ent.Attr.Attr.Size() != int64(len(payload)) {
			t.Fatalf("readdirplus size %d", ent.Attr.Attr.Size())
		}
	}
	if !seen["data.bin"] || !seen["app"] {
		t.Fatalf("readdirplus entries: %v", seen)
	}
	fi, _, err := target.Lookup("/app/config/db")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != 6 {
		t.Fatalf("lookup size %d", fi.Size())
	}

	// mkdir / create / rename / remove / rmdir
	if _, err := target.Mkdir("/d", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.value(t, "d/"); !ok {
		t.Fatal("mkdir did not write marker")
	}
	if _, err := target.Create("/d/x", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := target.RmDir("/d"); err == nil {
		t.Fatal("rmdir of non-empty directory succeeded")
	}
	if err := target.Rename("/d/x", "/d/y"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.value(t, "d/y"); !ok {
		t.Fatal("rename target missing")
	}
	if err := target.Remove("/d/y"); err != nil {
		t.Fatal(err)
	}
	if err := target.RmDir("/d"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := target.Lookup("/d", false); err == nil {
		t.Fatal("/d still exists")
	}

	// Directory rename over the wire.
	if _, err := target.Mkdir("/src", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Create("/src/a", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := target.Rename("/src", "/dst"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.value(t, "dst/a"); !ok {
		t.Fatal("directory rename did not move children")
	}
}

func TestE2ESubdirectoryMount(t *testing.T) {
	e := newEnv(t)
	if _, err := e.cli.Put(context.Background(), e.prefix+"app/config/db", "host=x"); err != nil {
		t.Fatal(err)
	}
	target, err := e.mount(t, "/app/config")
	if err != nil {
		t.Fatal(err)
	}
	r, err := target.Open("/db")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	r.Close()
	if string(b) != "host=x" {
		t.Fatalf("got %q", b)
	}
	if _, err := target.Create("/new", 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.value(t, "app/config/new"); !ok {
		t.Fatal("create via sub-mount landed elsewhere")
	}

	if _, err := e.mount(t, "/does/not/exist"); err == nil {
		t.Fatal("mounting a missing directory succeeded")
	}
}

func TestE2ELongPaths(t *testing.T) {
	e := newEnv(t)
	target, err := e.mount(t, "/")
	if err != nil {
		t.Fatal(err)
	}
	long := "/" + strings.Repeat("d", 40) + "/" + strings.Repeat("f", 40) // > 63 bytes
	if _, err := target.Mkdir("/"+strings.Repeat("d", 40), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := target.OpenFile(long, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("long")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	fi, _, err := target.Lookup(long, false)
	if err != nil || fi.Size() != 4 {
		t.Fatalf("lookup long path: %v %v", fi, err)
	}
}

func TestFSStat(t *testing.T) {
	e := newEnv(t)
	h := New(e.fs, Options{})
	st := nfs.FSStat{AvailableSize: 1, AvailableFiles: 1}
	if err := h.FSStat(context.Background(), e.fs, &st); err != nil {
		t.Fatal(err)
	}
	if st.TotalSize == 0 || st.FreeSize == 0 || st.FreeSize > st.TotalSize || st.AvailableSize != st.FreeSize {
		t.Fatalf("fsstat: %+v", st)
	}
}
