package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"

	nfsc "github.com/willscott/go-nfs-client/nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
)

// These tests need a local sshd on 127.0.0.1:2222 accepting the key in
// $SSHNFS_TEST_KEY for user $SSHNFS_TEST_USER.
func testConfig(t *testing.T, export string) *Config {
	t.Helper()
	key := os.Getenv("SSHNFS_TEST_KEY")
	user := os.Getenv("SSHNFS_TEST_USER")
	if key == "" || user == "" {
		t.Skip("SSHNFS_TEST_KEY / SSHNFS_TEST_USER not set")
	}
	cfg := DefaultConfig()
	cfg.Host = "127.0.0.1"
	cfg.Port = 2222
	cfg.User = user
	cfg.IdentityFiles = stringList{key}
	cfg.UseAgent = false
	cfg.InsecureHostKey = true
	cfg.RemotePath = export
	cfg.Listen = "127.0.0.1:0"
	cfg.HandleCache = 1024
	cfg.AttrCacheTTL = Duration(0) // exercise the uncached paths
	cfg.DirCacheTTL = Duration(0)
	return cfg
}

func startServer(t *testing.T, cfg *Config) (*nfsc.Target, func()) {
	t.Helper()
	log := NewLogger(LevelWarn)
	conn := NewConn(cfg, log)

	root, err := resolveRoot(conn, cfg)
	if err != nil {
		conn.Close()
		t.Fatalf("connect: %v", err)
	}
	fsys := NewFS(cfg, conn, log, root)
	handler := &statHandler{
		Handler: nfshelper.NewCachingHandler(nfshelper.NewNullAuthHandler(fsys), cfg.HandleCache),
		fs:      fsys,
		cfg:     cfg,
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = nfs.Serve(listener, handler) }()

	// go-nfs-client picks its own source port, which occasionally collides.
	var c *rpc.Client
	for attempt := 0; attempt < 5; attempt++ {
		c, err = rpc.DialTCP("tcp", listener.Addr().String(), false)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("rpc dial: %v", err)
	}
	var mounter nfsc.Mount
	mounter.Client = c
	target, err := mounter.Mount("/", rpc.AuthNull)
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	return target, func() {
		_ = mounter.Unmount()
		c.Close()
		listener.Close()
		fsys.Close()
		conn.Close()
	}
}

func TestReadWriteRoundTrip(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	target, stop := startServer(t, cfg)
	defer stop()

	if _, err := target.FSInfo(); err != nil {
		t.Fatalf("fsinfo: %v", err)
	}

	if _, err := target.Create("/hello.txt", 0o644); err != nil {
		t.Fatalf("create: %v", err)
	}
	fi, err := os.Stat(filepath.Join(export, "hello.txt"))
	if err != nil {
		t.Fatalf("file was not created on the server: %v", err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0644", fi.Mode().Perm())
	}

	// A payload bigger than one NFS write so several RPCs hit the same
	// pooled sftp handle.
	payload := bytes.Repeat([]byte("0123456789abcdef"), 64*1024) // 1 MiB
	w, err := target.OpenFile("/hello.txt", 0o644)
	if err != nil {
		t.Fatalf("open for write: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.Close()

	onDisk, err := os.ReadFile(filepath.Join(export, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, payload) {
		t.Fatalf("server side content mismatch: got %d bytes, want %d", len(onDisk), len(payload))
	}

	r, err := target.Open("/hello.txt")
	if err != nil {
		t.Fatalf("open for read: %v", err)
	}
	defer r.Close()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(r, got); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("read back content mismatch")
	}
}

func TestDirectoryOperations(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	target, stop := startServer(t, cfg)
	defer stop()

	if _, err := target.Mkdir("/sub", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// go-nfs emits "." and ".." on the wire; the test client strips them.
	var want []string
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if _, err := target.Create("/sub/"+name, 0o600); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		want = append(want, name)
	}

	entries, err := target.ReadDirPlus("/sub")
	if err != nil {
		t.Fatalf("readdirplus: %v", err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("readdirplus = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("readdirplus = %v, want %v", got, want)
		}
	}

	if err := target.Rename("/sub/a.txt", "/sub/renamed.txt"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(export, "sub", "renamed.txt")); err != nil {
		t.Fatalf("rename did not take effect: %v", err)
	}

	if err := target.Remove("/sub/renamed.txt"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(export, "sub", "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still present after remove: %v", err)
	}

	if err := target.RmDir("/sub"); err == nil {
		t.Fatal("expected rmdir of a non-empty directory to fail")
	}
	if err := target.RemoveAll("/sub"); err != nil {
		t.Fatalf("removeall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(export, "sub")); !os.IsNotExist(err) {
		t.Fatalf("directory still present: %v", err)
	}
}

func TestSetattrAndSymlink(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	target, stop := startServer(t, cfg)
	defer stop()

	if _, err := target.Create("/f.txt", 0o600); err != nil {
		t.Fatalf("create: %v", err)
	}
	mode := uint32(0o640)
	size := uint64(4096)
	mtime := nfsc.NFS3Time{Seconds: uint32(time.Now().Add(-time.Hour).Unix())}
	if err := target.Setattr("/f.txt", nfsc.Sattr3{
		Mode:  nfsc.SetMode{SetIt: true, Mode: mode},
		Size:  nfsc.SetSize{SetIt: true, Size: size},
		Mtime: nfsc.SetTime{SetIt: nfsc.SetToClientTime, Time: mtime},
	}); err != nil {
		t.Fatalf("setattr: %v", err)
	}
	fi, err := os.Stat(filepath.Join(export, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != os.FileMode(mode) {
		t.Fatalf("mode = %v, want %v", fi.Mode().Perm(), os.FileMode(mode))
	}
	if fi.Size() != int64(size) {
		t.Fatalf("size = %d, want %d", fi.Size(), size)
	}

	if err := target.Symlink("target.txt", "/link"); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	link, err := os.Readlink(filepath.Join(export, "link"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if link != "target.txt" {
		t.Fatalf("link target = %q, want %q", link, "target.txt")
	}
}

func TestReadOnlyExport(t *testing.T) {
	export := t.TempDir()
	if err := os.WriteFile(filepath.Join(export, "data.txt"), []byte("read me"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, export)
	cfg.ReadOnly = true
	target, stop := startServer(t, cfg)
	defer stop()

	f, err := target.Open("/data.txt")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	buf := make([]byte, 7)
	if _, err := io.ReadFull(f, buf); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read: %v", err)
	}
	f.Close()
	if string(buf) != "read me" {
		t.Fatalf("content = %q", buf)
	}

	if _, err := target.Create("/nope.txt", 0o644); err == nil {
		t.Fatal("create succeeded on a read-only export")
	}
	if _, err := os.Stat(filepath.Join(export, "nope.txt")); !os.IsNotExist(err) {
		t.Fatal("read-only export was written to")
	}
}

func TestAttributeCaching(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	cfg.AttrCacheTTL = Duration(2 * time.Second)
	cfg.DirCacheTTL = Duration(2 * time.Second)
	target, stop := startServer(t, cfg)
	defer stop()

	if _, err := target.Create("/cached.txt", 0o644); err != nil {
		t.Fatalf("create: %v", err)
	}
	w, err := target.OpenFile("/cached.txt", 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("some bytes")); err != nil {
		t.Fatal(err)
	}
	w.Close()

	// A write must invalidate the cached size immediately.
	attr, err := target.Getattr("/cached.txt")
	if err != nil {
		t.Fatalf("getattr: %v", err)
	}
	if attr.Filesize != 10 {
		t.Fatalf("cached size = %d, want 10", attr.Filesize)
	}
}

func TestUIDSquashing(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	cfg.UID = 4242
	cfg.GID = 4243
	target, stop := startServer(t, cfg)
	defer stop()

	if _, err := target.Create("/owned.txt", 0o644); err != nil {
		t.Fatalf("create: %v", err)
	}
	attr, err := target.Getattr("/owned.txt")
	if err != nil {
		t.Fatalf("getattr: %v", err)
	}
	if attr.UID != 4242 || attr.GID != 4243 {
		t.Fatalf("uid/gid = %d/%d, want 4242/4243", attr.UID, attr.GID)
	}
}

func TestPathEscapeIsContained(t *testing.T) {
	export := t.TempDir()
	fsys := &FS{root: normalizeRoot(export)}
	for _, in := range []string{"../../etc/passwd", "/../etc/passwd", "a/../../../etc/passwd"} {
		got := fsys.resolve(in)
		if got != filepath.Join(export, "etc", "passwd") && got != export {
			t.Fatalf("resolve(%q) = %q, escaped the export root", in, got)
		}
	}
}

// TestReconnectAfterSessionLoss simulates the SSH session dying underneath a
// live NFS export and checks that the next request transparently redials.
func TestReconnectAfterSessionLoss(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	log := NewLogger(LevelWarn)
	conn := NewConn(cfg, log)
	defer conn.Close()

	root, err := resolveRoot(conn, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	fsys := NewFS(cfg, conn, log, root)
	defer fsys.Close()

	if err := os.WriteFile(filepath.Join(export, "before.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := fsys.Open("before.txt")
	if err != nil {
		t.Fatalf("open before drop: %v", err)
	}
	buf := make([]byte, 6)
	if _, err := io.ReadFull(f, buf); err != nil {
		t.Fatalf("read before drop: %v", err)
	}
	f.Close()

	// Pull the rug out: exactly what a flaky link or an sshd restart does.
	_, gen, err := conn.Client()
	if err != nil {
		t.Fatal(err)
	}
	conn.Invalidate(gen)
	fsys.files.dropAll()
	fsys.attrs.clear()

	fi, err := fsys.Stat("before.txt")
	if err != nil {
		t.Fatalf("stat after drop (should have reconnected): %v", err)
	}
	if fi.Size() != 6 {
		t.Fatalf("size after reconnect = %d, want 6", fi.Size())
	}

	w, err := fsys.Create("after.txt")
	if err != nil {
		t.Fatalf("create after reconnect: %v", err)
	}
	if _, err := w.Write([]byte("after")); err != nil {
		t.Fatalf("write after reconnect: %v", err)
	}
	w.Close()
	got, err := os.ReadFile(filepath.Join(export, "after.txt"))
	if err != nil || string(got) != "after" {
		t.Fatalf("content after reconnect = %q, %v", got, err)
	}
}
