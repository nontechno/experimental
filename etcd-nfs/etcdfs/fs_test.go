package etcdfs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// Tests run against a real etcd. Set ETCD_ENDPOINTS (e.g.
// http://127.0.0.1:2379); each test uses its own random prefix.

func testClient(t *testing.T) *clientv3.Client {
	t.Helper()
	eps := os.Getenv("ETCD_ENDPOINTS")
	if eps == "" {
		t.Skip("ETCD_ENDPOINTS not set")
	}
	cli, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(eps, ","), DialTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	return cli
}

func testFS(t *testing.T, mutate ...func(*Options)) (*FS, *clientv3.Client, string) {
	t.Helper()
	cli := testClient(t)
	var b [4]byte
	_, _ = rand.Read(b[:])
	prefix := "/etcdnfs-test/" + t.Name() + "-" + hex.EncodeToString(b[:]) + "/"
	o := Options{Prefix: prefix, MaxFileSize: 64 << 10}
	for _, m := range mutate {
		m(&o)
	}
	f, err := New(cli, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = cli.Delete(context.Background(), prefix, clientv3.WithPrefix())
	})
	return f, cli, prefix
}

func put(t *testing.T, cli *clientv3.Client, key, val string) {
	t.Helper()
	if _, err := cli.Put(context.Background(), key, val); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, cli *clientv3.Client, key string) (string, bool) {
	t.Helper()
	r, err := cli.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Kvs) == 0 {
		return "", false
	}
	return string(r.Kvs[0].Value), true
}

func names(t *testing.T, f *FS, dir string) []string {
	t.Helper()
	infos, err := f.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	var out []string
	for _, fi := range infos {
		n := fi.Name()
		if fi.IsDir() {
			n += "/"
		}
		out = append(out, n)
	}
	return out
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func isErrno(err error, e syscall.Errno) bool { return errors.Is(err, e) }

func TestReadWrite(t *testing.T) {
	f, cli, p := testFS(t)

	fh, err := f.Create("/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := fh.Write([]byte(", world")); err != nil { // sequential position
		t.Fatal(err)
	}
	fh.Close()
	v, _ := get(t, cli, p+"hello.txt")
	eq(t, v, "hello, world")

	st, err := f.Stat("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, st.Size(), 12)
	eq(t, st.IsDir(), false)

	// Positional write past EOF zero-fills the gap (NFS WRITE semantics).
	fh, _ = f.OpenFile("hello.txt", os.O_RDWR, 0)
	fh.Seek(14, io.SeekStart)
	fh.Write([]byte("!"))
	fh.Close()
	v, _ = get(t, cli, p+"hello.txt")
	eq(t, []byte(v), []byte("hello, world\x00\x00!"))

	// ReadAt with EOF.
	fh, _ = f.Open("hello.txt")
	buf := make([]byte, 10)
	n, err := fh.ReadAt(buf, 7)
	eq(t, n, 8)
	if err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
	fh.Close()

	// Append mode.
	fh, _ = f.OpenFile("hello.txt", os.O_WRONLY|os.O_APPEND, 0)
	fh.Write([]byte("?"))
	fh.Close()
	st, _ = f.Stat("hello.txt")
	eq(t, st.Size(), 16)

	// Truncate shrink and grow.
	fh, _ = f.OpenFile("hello.txt", os.O_WRONLY, 0)
	if err := fh.Truncate(5); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	v, _ = get(t, cli, p+"hello.txt")
	eq(t, v, "hello")
	fh, _ = f.OpenFile("hello.txt", os.O_WRONLY, 0)
	fh.Truncate(7)
	fh.Close()
	v, _ = get(t, cli, p+"hello.txt")
	eq(t, []byte(v), []byte("hello\x00\x00"))

	// O_TRUNC on open.
	fh, _ = f.OpenFile("hello.txt", os.O_WRONLY|os.O_TRUNC, 0)
	fh.Close()
	v, ok := get(t, cli, p+"hello.txt")
	eq(t, v, "")
	eq(t, ok, true)

	// Size limit.
	fh, _ = f.OpenFile("hello.txt", os.O_WRONLY, 0)
	_, err = fh.Write(make([]byte, 64<<10+1))
	if !isErrno(err, syscall.EFBIG) {
		t.Fatalf("want EFBIG, got %v", err)
	}
	fh.Close()

	// Exclusive create.
	if _, err := f.OpenFile("hello.txt", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0); !errors.Is(err, os.ErrExist) {
		t.Fatalf("want ErrExist, got %v", err)
	}
	// Open missing.
	if _, err := f.Open("nope"); !os.IsNotExist(err) {
		t.Fatalf("want not exist, got %v", err)
	}
}

func TestImplicitDirectories(t *testing.T) {
	f, cli, p := testFS(t)
	put(t, cli, p+"app/config/db", "host=x")
	put(t, cli, p+"app/config/cache", "ttl=5")
	put(t, cli, p+"app/name", "demo")
	put(t, cli, p+"app-x", "sorts between app and app/")
	put(t, cli, p+"top", "1")

	eq(t, names(t, f, "/"), []string{"app/", "app-x", "top"})
	eq(t, names(t, f, "app"), []string{"config/", "name"})
	eq(t, names(t, f, "app/config"), []string{"cache", "db"})

	st, err := f.Stat("app/config")
	if err != nil || !st.IsDir() {
		t.Fatalf("app/config should be a dir: %v %v", st, err)
	}
	infos, _ := f.ReadDir("app/config")
	eq(t, infos[1].Size(), 6)

	// A file can't be created under a file.
	if _, err := f.Create("top/child"); !isErrno(err, syscall.ENOTDIR) {
		t.Fatalf("want ENOTDIR, got %v", err)
	}
	// Or under a missing directory.
	if _, err := f.Create("missing/child"); !os.IsNotExist(err) {
		t.Fatalf("want not exist, got %v", err)
	}
	// A file can't be created where a directory exists.
	if _, err := f.Create("app"); !isErrno(err, syscall.EISDIR) {
		t.Fatalf("want EISDIR, got %v", err)
	}
}

func TestShadowedAndUnrepresentableKeys(t *testing.T) {
	f, cli, p := testFS(t)
	put(t, cli, p+"s", "shadowed")
	put(t, cli, p+"s/t", "child")
	put(t, cli, p+"w//x", "empty segment")
	put(t, cli, p+"w/./y", "dot segment")
	put(t, cli, p+"w/ok", "fine")

	eq(t, names(t, f, "/"), []string{"s/", "w/"})
	eq(t, names(t, f, "w"), []string{"ok"})
	st, _ := f.Stat("s")
	eq(t, st.IsDir(), true)
}

func TestMkdirRemove(t *testing.T) {
	f, cli, p := testFS(t)

	if err := f.MkdirAll("a/b/c", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := get(t, cli, p+"a/b/c/"); !ok {
		t.Fatal("marker a/b/c/ not written")
	}
	eq(t, names(t, f, "a/b/c"), []string{})
	eq(t, names(t, f, "a/b"), []string{"c/"})

	fh, _ := f.Create("a/b/c/file")
	fh.Close()

	// Non-empty directory.
	if err := f.Remove("a/b/c"); !isErrno(err, syscall.ENOTEMPTY) {
		t.Fatalf("want ENOTEMPTY, got %v", err)
	}
	if err := f.Remove("a/b/c/file"); err != nil {
		t.Fatal(err)
	}
	// Now empty (only its marker remains): removable.
	if err := f.Remove("a/b/c"); err != nil {
		t.Fatalf("remove empty dir: %v", err)
	}
	if _, err := f.Stat("a/b/c"); !os.IsNotExist(err) {
		t.Fatalf("a/b/c should be gone: %v", err)
	}
	// a/b had no marker of its own (MkdirAll("a/b/c") only wrote the leaf),
	// but it must survive losing its last child.
	st, err := f.Stat("a/b")
	if err != nil || !st.IsDir() {
		t.Fatalf("a/b should still exist: %v", err)
	}
	if err := f.MkdirAll("a/b", 0o755); err != nil {
		t.Fatal(err)
	}
	fh, _ = f.Create("plain")
	fh.Close()
	if err := f.MkdirAll("plain", 0o755); !isErrno(err, syscall.ENOTDIR) {
		t.Fatalf("want ENOTDIR, got %v", err)
	}
	if err := f.Remove("nope"); !os.IsNotExist(err) {
		t.Fatalf("want not exist, got %v", err)
	}
}

func TestRemoveKeepsImplicitParent(t *testing.T) {
	f, cli, p := testFS(t)
	put(t, cli, p+"dir/only", "x")
	if err := f.Remove("dir/only"); err != nil {
		t.Fatal(err)
	}
	st, err := f.Stat("dir")
	if err != nil || !st.IsDir() {
		t.Fatalf("dir vanished after removing its last entry: %v", err)
	}
	if _, ok := get(t, cli, p+"dir/"); !ok {
		t.Fatal("parent marker not created")
	}
}

func TestRenameFile(t *testing.T) {
	f, cli, p := testFS(t)
	put(t, cli, p+"d/a", "A")
	put(t, cli, p+"d/b", "B")
	put(t, cli, p+"e/keep", "")

	if err := f.Rename("d/a", "e/a2"); err != nil {
		t.Fatal(err)
	}
	v, _ := get(t, cli, p+"e/a2")
	eq(t, v, "A")
	if _, ok := get(t, cli, p+"d/a"); ok {
		t.Fatal("source still present")
	}
	// Overwrite existing destination.
	if err := f.Rename("d/b", "e/a2"); err != nil {
		t.Fatal(err)
	}
	v, _ = get(t, cli, p+"e/a2")
	eq(t, v, "B")
	// d lost its last entry but still exists.
	if st, err := f.Stat("d"); err != nil || !st.IsDir() {
		t.Fatalf("d should remain: %v", err)
	}
	// Onto a directory.
	if err := f.Rename("e/a2", "d"); !isErrno(err, syscall.EISDIR) {
		t.Fatalf("want EISDIR, got %v", err)
	}
	// Into a missing directory.
	if err := f.Rename("e/a2", "zz/a"); !os.IsNotExist(err) {
		t.Fatalf("want not exist, got %v", err)
	}
}

func TestRenamePreservesLease(t *testing.T) {
	f, cli, p := testFS(t)
	ctx := context.Background()
	lease, err := cli.Grant(ctx, 600)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Revoke(ctx, lease.ID)
	if _, err := cli.Put(ctx, p+"leased", "v", clientv3.WithLease(lease.ID)); err != nil {
		t.Fatal(err)
	}
	fh, _ := f.OpenFile("leased", os.O_WRONLY, 0)
	fh.Write([]byte("w"))
	fh.Close()
	if err := f.Rename("leased", "moved"); err != nil {
		t.Fatal(err)
	}
	r, _ := cli.Get(ctx, p+"moved")
	eq(t, clientv3.LeaseID(r.Kvs[0].Lease), lease.ID)
}

func TestRenameDir(t *testing.T) {
	f, cli, p := testFS(t)
	put(t, cli, p+"src/", "")
	put(t, cli, p+"src/x", "1")
	put(t, cli, p+"src/sub/y", "2")
	put(t, cli, p+"nonempty/z", "3")
	put(t, cli, p+"empty/", "")

	if err := f.Rename("src", "nonempty"); !isErrno(err, syscall.ENOTEMPTY) {
		t.Fatalf("want ENOTEMPTY, got %v", err)
	}
	if err := f.Rename("src", "src/sub/inside"); !isErrno(err, syscall.EINVAL) {
		t.Fatalf("want EINVAL, got %v", err)
	}
	if err := f.Rename("src/sub", "src"); !isErrno(err, syscall.ENOTEMPTY) {
		t.Fatalf("want ENOTEMPTY for move onto ancestor, got %v", err)
	}
	if err := f.Rename("src", "empty"); err != nil {
		t.Fatal(err)
	}
	eq(t, names(t, f, "empty"), []string{"sub/", "x"})
	v, _ := get(t, cli, p+"empty/sub/y")
	eq(t, v, "2")
	if _, err := f.Stat("src"); !os.IsNotExist(err) {
		t.Fatalf("src should be gone: %v", err)
	}
	// Rename to a fresh name, implicit source (no marker).
	if err := f.Rename("empty/sub", "fresh"); err != nil {
		t.Fatal(err)
	}
	eq(t, names(t, f, "fresh"), []string{"y"})
	eq(t, names(t, f, "empty"), []string{"x"})
}

func TestRenameDirTooLarge(t *testing.T) {
	f, cli, p := testFS(t, func(o *Options) { o.MaxTxnOps = 8 })
	for i := 0; i < 10; i++ {
		put(t, cli, fmt.Sprintf("%sbig/%02d", p, i), "v")
	}
	if err := f.Rename("big", "big2"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	eq(t, len(names(t, f, "big")), 10)
}

func TestReadDirPagingAndSkipping(t *testing.T) {
	f, cli, p := testFS(t)
	ctx := context.Background()
	var ops []clientv3.Op
	flush := func() {
		if len(ops) == 0 {
			return
		}
		if _, err := cli.Txn(ctx).Then(ops...).Commit(); err != nil {
			t.Fatal(err)
		}
		ops = ops[:0]
	}
	add := func(k string) {
		ops = append(ops, clientv3.OpPut(p+k, "xy"))
		if len(ops) == 100 {
			flush()
		}
	}
	// 1500 descendants in one subtree, interleaved (by sort order) with
	// 1200 direct files, so pages end both inside and outside subtrees.
	for i := 0; i < 1500; i++ {
		add(fmt.Sprintf("m/deep/%04d/leaf", i))
	}
	for i := 0; i < 1200; i++ {
		add(fmt.Sprintf("m/f%04d", i))
	}
	for i := 0; i < 5; i++ {
		add(fmt.Sprintf("m/z%d/a", i))
	}
	flush()

	got := names(t, f, "m")
	eq(t, len(got), 1+1200+5)
	if !sort.StringsAreSorted(got) {
		t.Fatal("not sorted")
	}
	infos, _ := f.ReadDir("m")
	for _, fi := range infos {
		if !fi.IsDir() && fi.Size() != 2 {
			t.Fatalf("%s: size %d", fi.Name(), fi.Size())
		}
	}
	eq(t, len(names(t, f, "m/deep")), 1500)
}

func TestConcurrentAppends(t *testing.T) {
	f, _, _ := testFS(t)
	fh, _ := f.Create("log")
	fh.Close()
	const writers, each = 8, 10
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				h, err := f.OpenFile("log", os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := h.Write([]byte{byte('a' + w)}); err != nil {
					t.Error(err)
				}
				h.Close()
			}
		}(w)
	}
	wg.Wait()
	st, _ := f.Stat("log")
	eq(t, st.Size(), writers*each)
}

func TestTimesTrackRevisions(t *testing.T) {
	f, cli, p := testFS(t)
	put(t, cli, p+"a", "1")
	put(t, cli, p+"b", "1")
	a1, _ := f.Stat("a")
	b1, _ := f.Stat("b")
	time.Sleep(2 * time.Millisecond)
	put(t, cli, p+"a", "2")
	a2, _ := f.Stat("a")
	b2, _ := f.Stat("b")
	if !a2.ModTime().After(a1.ModTime()) {
		t.Fatalf("mtime did not advance: %v -> %v", a1.ModTime(), a2.ModTime())
	}
	eq(t, b2.ModTime().Equal(b1.ModTime()), true)

	// ReadDir reports the same times as Stat.
	infos, _ := f.ReadDir("/")
	for _, fi := range infos {
		if fi.Name() == "a" && !fi.ModTime().Equal(a2.ModTime()) {
			t.Fatalf("readdir mtime %v != stat mtime %v", fi.ModTime(), a2.ModTime())
		}
	}
}

func TestClockCompaction(t *testing.T) {
	c := newRevClock(4)
	for r := int64(1); r <= 20; r++ {
		c.observe(r)
	}
	prev := time.Time{}
	for r := int64(1); r <= 20; r++ {
		tm := c.timeOf(r, 20)
		if tm.Before(prev) {
			t.Fatalf("time went backwards at %d", r)
		}
		prev = tm
	}
	if len(c.revs) > 4 {
		t.Fatalf("table not bounded: %d", len(c.revs))
	}
}

func TestChrootViewsShareIdentity(t *testing.T) {
	f, cli, p := testFS(t)
	put(t, cli, p+"app/config/db", "x")
	v, err := f.Chroot("app")
	if err != nil {
		t.Fatal(err)
	}
	viaView, err := v.Stat("config/db")
	if err != nil {
		t.Fatal(err)
	}
	viaRoot, _ := f.Stat("app/config/db")
	eq(t, viaView.Sys(), viaRoot.Sys())
	eq(t, v.Root(), p+"app/")

	fh, _ := v.Create("config/new")
	fh.Close()
	if _, ok := get(t, cli, p+"app/config/new"); !ok {
		t.Fatal("chroot create landed elsewhere")
	}
}

func TestReadOnlyAndDeny(t *testing.T) {
	ro, cli, p := testFS(t, func(o *Options) { o.ReadOnly = true })
	put(t, cli, p+"f", "x")
	if _, err := ro.OpenFile("f", os.O_WRONLY, 0); !isErrno(err, syscall.EROFS) {
		t.Fatalf("want EROFS, got %v", err)
	}
	if err := ro.Remove("f"); !isErrno(err, syscall.EROFS) {
		t.Fatalf("want EROFS, got %v", err)
	}
	fh, err := ro.Open("f")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(fh)
	eq(t, string(b), "x")

	deny, _, _ := testFS(t, func(o *Options) {
		o.Deny = func(n string) bool { return n == ".DS_Store" || strings.HasPrefix(n, "._") }
	})
	if _, err := deny.Create(".DS_Store"); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("want permission error, got %v", err)
	}
	fh, _ = deny.Create("ok")
	fh.Close()
	if err := deny.Rename("ok", "._ok"); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("want permission error, got %v", err)
	}
}

func TestTempFile(t *testing.T) {
	f, _, _ := testFS(t)
	fh, err := f.TempFile("", "tmp-")
	if err != nil {
		t.Fatal(err)
	}
	fh.Write([]byte("data"))
	fh.Close()
	st, err := f.Stat(fh.Name())
	if err != nil {
		t.Fatal(err)
	}
	eq(t, st.Size(), 4)
	if !bytes.HasPrefix([]byte(st.Name()), []byte("tmp-")) {
		t.Fatal(st.Name())
	}
}
