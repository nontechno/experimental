package boltfs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func newTestFS(t *testing.T, opts Options) (*FS, *bolt.DB) {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "test.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if opts.RootBucket == "" {
		opts.RootBucket = "root"
	}
	fsys, err := New(db, opts)
	if err != nil {
		t.Fatal(err)
	}
	return fsys, db
}

func writeFile(t *testing.T, f *FS, name, content string) {
	t.Helper()
	fh, err := f.Create(name)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if _, err := fh.Write([]byte(content)); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := fh.Close(); err != nil {
		t.Fatalf("close %s: %v", name, err)
	}
}

func readFile(t *testing.T, f *FS, name string) string {
	t.Helper()
	fh, err := f.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer fh.Close()
	b, err := io.ReadAll(fh)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func wantErrno(t *testing.T, err error, target error, what string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: got %v, want %v", what, err, target)
	}
}

func TestFileRoundTrip(t *testing.T) {
	f, db := newTestFS(t, Options{})
	writeFile(t, f, "/hello.txt", "hello world")

	if got := readFile(t, f, "/hello.txt"); got != "hello world" {
		t.Fatalf("content = %q", got)
	}
	st, err := f.Stat("/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if st.IsDir() || st.Size() != 11 || st.Name() != "hello.txt" {
		t.Fatalf("stat = %+v", st)
	}

	// The value must be exactly the file content, with no framing.
	if err := db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte("root")).Get([]byte("hello.txt"))
		if string(v) != "hello world" {
			t.Fatalf("stored value = %q", v)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// An empty value and a nested bucket are both reported as a nil value by a
// bbolt cursor. Empty files must still be files.
func TestEmptyFileIsNotADirectory(t *testing.T) {
	f, _ := newTestFS(t, Options{})
	fh, err := f.Create("/empty")
	if err != nil {
		t.Fatal(err)
	}
	fh.Close()

	st, err := f.Stat("/empty")
	if err != nil {
		t.Fatal(err)
	}
	if st.IsDir() {
		t.Fatal("empty file reported as a directory")
	}
	if st.Size() != 0 {
		t.Fatalf("size = %d", st.Size())
	}
	ents, err := f.ReadDir("/")
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].IsDir() {
		t.Fatalf("readdir = %+v", ents)
	}
	if got := readFile(t, f, "/empty"); got != "" {
		t.Fatalf("content = %q", got)
	}
}

func TestDirectoriesAreBuckets(t *testing.T) {
	f, db := newTestFS(t, Options{})
	if err := f.MkdirAll("/a/b/c", 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, f, "/a/b/c/leaf", "x")

	if err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("root")).Bucket([]byte("a")).Bucket([]byte("b")).Bucket([]byte("c"))
		if b == nil {
			t.Fatal("nested buckets not created")
		}
		if string(b.Get([]byte("leaf"))) != "x" {
			t.Fatal("leaf value missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	st, err := f.Stat("/a/b")
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsDir() {
		t.Fatal("bucket not reported as directory")
	}
	ents, err := f.ReadDir("/a/b/c")
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name() != "leaf" {
		t.Fatalf("readdir = %+v", ents)
	}
}

func TestPreexistingDatabaseIsVisible(t *testing.T) {
	f, db := newTestFS(t, Options{})
	if err := db.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket([]byte("root"))
		sub, err := root.CreateBucket([]byte("config"))
		if err != nil {
			return err
		}
		return sub.Put([]byte("db.yaml"), []byte("host: localhost\n"))
	}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f, "/config/db.yaml"); got != "host: localhost\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestWriteOffsetsAppendAndTruncate(t *testing.T) {
	f, _ := newTestFS(t, Options{})
	writeFile(t, f, "/f", "aaaabbbb")

	// Overwrite in the middle.
	fh, err := f.OpenFile("/f", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.Seek(4, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := fh.Write([]byte("CCCC")); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	if got := readFile(t, f, "/f"); got != "aaaaCCCC" {
		t.Fatalf("after overwrite = %q", got)
	}

	// Write past the end zero-fills the gap.
	fh, _ = f.OpenFile("/f", os.O_RDWR, 0)
	fh.Seek(10, io.SeekStart)
	fh.Write([]byte("Z"))
	fh.Close()
	got := readFile(t, f, "/f")
	if len(got) != 11 || got[8:10] != "\x00\x00" || got[10] != 'Z' {
		t.Fatalf("after sparse write = %q", got)
	}

	// Append.
	fh, _ = f.OpenFile("/f", os.O_WRONLY|os.O_APPEND, 0)
	fh.Write([]byte("!"))
	fh.Close()
	if got := readFile(t, f, "/f"); len(got) != 12 || got[11] != '!' {
		t.Fatalf("after append = %q", got)
	}

	// Truncate down, then up.
	fh, _ = f.OpenFile("/f", os.O_RDWR, 0)
	if err := fh.Truncate(4); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	if got := readFile(t, f, "/f"); got != "aaaa" {
		t.Fatalf("after truncate = %q", got)
	}
	fh, _ = f.OpenFile("/f", os.O_RDWR, 0)
	fh.Truncate(6)
	fh.Close()
	if got := readFile(t, f, "/f"); got != "aaaa\x00\x00" {
		t.Fatalf("after grow = %q", got)
	}

	// O_TRUNC on open.
	fh, err = f.OpenFile("/f", os.O_RDWR|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	fh.Close()
	if got := readFile(t, f, "/f"); got != "" {
		t.Fatalf("after O_TRUNC = %q", got)
	}
}

func TestOpenFlags(t *testing.T) {
	f, _ := newTestFS(t, Options{})
	if _, err := f.Open("/missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open missing: %v", err)
	}
	fh, err := f.OpenFile("/x", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0)
	if err != nil {
		t.Fatal(err)
	}
	fh.Close()
	if _, err := f.OpenFile("/x", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0); !errors.Is(err, os.ErrExist) {
		t.Fatalf("O_EXCL on existing: %v", err)
	}
	if err := f.MkdirAll("/d", 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = f.Open("/d")
	wantErrno(t, err, syscall.EISDIR, "open directory")

	// go-nfs uses O_WRONLY|O_EXCL without O_CREATE for SETATTR size changes.
	fh, err = f.OpenFile("/x", os.O_WRONLY|os.O_EXCL, 0)
	if err != nil {
		t.Fatalf("O_WRONLY|O_EXCL on existing file: %v", err)
	}
	if err := fh.Truncate(0); err != nil {
		t.Fatal(err)
	}
	fh.Close()

	// A file cannot shadow a directory or vice versa.
	_, err = f.Create("/d")
	wantErrno(t, err, syscall.EISDIR, "create over directory")
	err = f.MkdirAll("/x/y", 0o755)
	wantErrno(t, err, syscall.ENOTDIR, "mkdir under a file")
}

func TestRemove(t *testing.T) {
	f, _ := newTestFS(t, Options{})
	writeFile(t, f, "/f", "x")
	if err := f.Remove("/f"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat("/f"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat after remove: %v", err)
	}
	if err := f.Remove("/f"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("double remove: %v", err)
	}

	f.MkdirAll("/d/sub", 0o755)
	err := f.Remove("/d")
	wantErrno(t, err, syscall.ENOTEMPTY, "remove non-empty directory")
	if err := f.Remove("/d/sub"); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove("/d"); err != nil {
		t.Fatal(err)
	}
}

func TestRename(t *testing.T) {
	f, _ := newTestFS(t, Options{})
	writeFile(t, f, "/a.txt", "A")
	if err := f.Rename("/a.txt", "/b.txt"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f, "/b.txt"); got != "A" {
		t.Fatalf("content = %q", got)
	}
	if _, err := f.Stat("/a.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source still present: %v", err)
	}

	// Overwrite an existing file.
	writeFile(t, f, "/c.txt", "C")
	if err := f.Rename("/b.txt", "/c.txt"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f, "/c.txt"); got != "A" {
		t.Fatalf("overwritten content = %q", got)
	}

	// Move a subtree.
	f.MkdirAll("/src/inner", 0o755)
	writeFile(t, f, "/src/one", "1")
	writeFile(t, f, "/src/inner/two", "2")
	f.MkdirAll("/dstparent", 0o755)
	if err := f.Rename("/src", "/dstparent/moved"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f, "/dstparent/moved/inner/two"); got != "2" {
		t.Fatalf("moved content = %q", got)
	}
	if _, err := f.Stat("/src"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source subtree still present: %v", err)
	}

	// Type mismatches and loops.
	writeFile(t, f, "/file", "x")
	err := f.Rename("/file", "/dstparent")
	wantErrno(t, err, syscall.EISDIR, "file onto directory")
	err = f.Rename("/dstparent", "/file")
	wantErrno(t, err, syscall.ENOTDIR, "directory onto file")
	err = f.Rename("/dstparent", "/dstparent/moved/deeper")
	wantErrno(t, err, syscall.EINVAL, "directory into itself")
	f.MkdirAll("/other", 0o755)
	writeFile(t, f, "/other/keep", "k")
	err = f.Rename("/dstparent", "/other")
	wantErrno(t, err, syscall.ENOTEMPTY, "directory onto non-empty directory")

	// Onto an empty directory is allowed.
	f.MkdirAll("/emptydst", 0o755)
	if err := f.Rename("/dstparent", "/emptydst"); err != nil {
		t.Fatalf("rename onto empty directory: %v", err)
	}
	if _, err := f.Stat("/emptydst/moved/one"); err != nil {
		t.Fatal(err)
	}
}

func TestLimits(t *testing.T) {
	f, _ := newTestFS(t, Options{MaxFileSize: 16})
	fh, err := f.Create("/big")
	if err != nil {
		t.Fatal(err)
	}
	_, err = fh.Write(make([]byte, 17))
	wantErrno(t, err, syscall.EFBIG, "write past max file size")
	if _, err := fh.Write(make([]byte, 16)); err != nil {
		t.Fatalf("write at max file size: %v", err)
	}
	err = fh.Truncate(17)
	wantErrno(t, err, syscall.EFBIG, "truncate past max file size")
	fh.Close()

	f2, _ := newTestFS(t, Options{MaxRenameEntries: 2})
	f2.MkdirAll("/d", 0o755)
	writeFile(t, f2, "/d/1", "a")
	writeFile(t, f2, "/d/2", "b")
	writeFile(t, f2, "/d/3", "c")
	err = f2.Rename("/d", "/e")
	wantErrno(t, err, syscall.EFBIG, "oversized directory rename")
	// The failed rename must have rolled back completely.
	if _, err := f2.Stat("/e"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial rename left /e: %v", err)
	}
	if got := readFile(t, f2, "/d/3"); got != "c" {
		t.Fatalf("source damaged: %q", got)
	}
}

func TestReadOnly(t *testing.T) {
	f, _ := newTestFS(t, Options{})
	writeFile(t, f, "/f", "x")

	ro, _ := New(f.db, Options{RootBucket: "root", ReadOnly: true})
	if got := readFile(t, ro, "/f"); got != "x" {
		t.Fatalf("read-only read = %q", got)
	}
	_, err := ro.Create("/new")
	wantErrno(t, err, syscall.EROFS, "create")
	wantErrno(t, ro.Remove("/f"), syscall.EROFS, "remove")
	wantErrno(t, ro.MkdirAll("/d", 0o755), syscall.EROFS, "mkdir")
	wantErrno(t, ro.Rename("/f", "/g"), syscall.EROFS, "rename")

	_, err = ro.OpenFile("/f", os.O_RDWR, 0)
	wantErrno(t, err, syscall.EROFS, "open for writing")

	// A handle opened for reading rejects writes as a read-only fd would.
	fh, err := ro.Open("/f")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	_, err = fh.Write([]byte("nope"))
	wantErrno(t, err, syscall.EBADF, "write through a read-only handle")
}

func TestDatabaseRootHoldsDirectoriesOnly(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "root.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f, err := New(db, Options{}) // no root bucket
	if err != nil {
		t.Fatal(err)
	}
	if err := f.MkdirAll("/top", 0o755); err != nil {
		t.Fatalf("mkdir at root: %v", err)
	}
	writeFile(t, f, "/top/file", "ok")

	_, err = f.Create("/toplevelfile")
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("file at database root: %v", err)
	}
	if ents, err := f.ReadDir("/"); err != nil || len(ents) != 1 || !ents[0].IsDir() {
		t.Fatalf("readdir root: %+v %v", ents, err)
	}
}

func TestUnrepresentableNamesAreSkipped(t *testing.T) {
	f, db := newTestFS(t, Options{})
	if err := db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("root"))
		if err := b.Put([]byte("a/b"), []byte("slash")); err != nil {
			return err
		}
		if err := b.Put([]byte("nul\x00name"), []byte("nul")); err != nil {
			return err
		}
		return b.Put([]byte("fine"), []byte("ok"))
	}); err != nil {
		t.Fatal(err)
	}
	ents, err := f.ReadDir("/")
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name() != "fine" {
		t.Fatalf("readdir = %+v", ents)
	}
	if _, err := f.Create("/bad\x00name"); !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("create with NUL: %v", err)
	}
}

func TestChrootAndIDs(t *testing.T) {
	f, _ := newTestFS(t, Options{})
	f.MkdirAll("/sub/deep", 0o755)
	writeFile(t, f, "/sub/deep/f", "v")

	subAny, err := f.Chroot("/sub")
	if err != nil {
		t.Fatal(err)
	}
	sub := subAny.(*FS)
	if got := readFile(t, sub, "/deep/f"); got != "v" {
		t.Fatalf("chroot read = %q", got)
	}
	if _, err := sub.Stat("/sub"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("chroot leaked the parent path")
	}
	outer, err := f.Stat("/sub/deep/f")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := sub.Stat("/deep/f")
	if err != nil {
		t.Fatal(err)
	}
	if fileID(f.absPath([]string{"sub", "deep", "f"}), false) != fileID(sub.absPath([]string{"deep", "f"}), false) {
		t.Fatal("file id differs between views")
	}
	if outer.Size() != inner.Size() {
		t.Fatal("size differs between views")
	}
}

func TestModTimeAdvancesOnChange(t *testing.T) {
	f, _ := newTestFS(t, Options{})
	writeFile(t, f, "/f", "1")
	first, err := f.Stat("/f")
	if err != nil {
		t.Fatal(err)
	}
	dirFirst, _ := f.Stat("/")

	fh, _ := f.OpenFile("/f", os.O_RDWR, 0)
	fh.Write([]byte("2"))
	fh.Close()

	second, _ := f.Stat("/f")
	if !second.ModTime().After(first.ModTime()) {
		t.Fatalf("mtime did not advance: %v -> %v", first.ModTime(), second.ModTime())
	}
	if !second.ModTime().After(dirFirst.ModTime()) {
		t.Fatal("directory mtime not ordered before the later write")
	}

	// An unchanged file keeps its timestamp.
	again, _ := f.Stat("/f")
	if !again.ModTime().Equal(second.ModTime()) {
		t.Fatal("mtime changed without a write")
	}

	// A removed name does not pass its timestamp to a new file.
	f.Remove("/f")
	writeFile(t, f, "/f", "3")
	third, _ := f.Stat("/f")
	if !third.ModTime().After(second.ModTime()) {
		t.Fatal("recreated file has a stale mtime")
	}
}

func TestConcurrentWritesAreSerialized(t *testing.T) {
	f, _ := newTestFS(t, Options{})
	const n = 8
	for i := 0; i < n; i++ {
		writeFile(t, f, "/f"+string(rune('a'+i)), "")
	}
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			name := "/f" + string(rune('a'+i))
			for j := 0; j < 20; j++ {
				fh, err := f.OpenFile(name, os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					done <- err
					return
				}
				if _, err := fh.Write([]byte("x")); err != nil {
					done <- err
					return
				}
				fh.Close()
			}
			done <- nil
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		if got := readFile(t, f, "/f"+string(rune('a'+i))); len(got) != 20 {
			t.Fatalf("lost writes: %d bytes", len(got))
		}
	}
}

func TestSizeLimitErrorNamesTheFlag(t *testing.T) {
	f, _ := newTestFS(t, Options{MaxFileSize: 16})
	fh, err := f.Create("/big")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	_, err = fh.Write(make([]byte, 17))
	if !errors.Is(err, syscall.EFBIG) {
		t.Fatalf("errno = %v, want EFBIG", err)
	}
	msg := err.Error()
	for _, want := range []string{"-max-file-size", "17", "16"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q does not mention %q", msg, want)
		}
	}

	f2, _ := newTestFS(t, Options{MaxRenameEntries: 1})
	f2.MkdirAll("/d", 0o755)
	writeFile(t, f2, "/d/1", "a")
	writeFile(t, f2, "/d/2", "b")
	err = f2.Rename("/d", "/e")
	if !errors.Is(err, syscall.EFBIG) || !strings.Contains(err.Error(), "-max-rename-entries") {
		t.Fatalf("rename error = %v", err)
	}
}
