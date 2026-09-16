package dbfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
	nfsc "github.com/willscott/go-nfs-client/nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
)

type fakeSource struct {
	mu      sync.Mutex
	objects map[string][]Object
	calls   atomic.Int64
	fail    map[string]error // file name -> error
	big     int
}

func newFake() *fakeSource {
	return &fakeSource{
		objects: map[string][]Object{
			"table": {{"HR", "EMPLOYEES"}, {"HR", "DEPT"}, {"WEIRD", "a/b.c%d"}},
			"view":  {{"HR", "EMP_V"}},
		},
		fail: map[string]error{},
	}
}

func (s *fakeSource) Kinds() []Kind {
	return []Kind{
		{Dir: "table", Description: "tables", Files: []string{"ddl.sql", "rows.csv", "big.txt"}},
		{Dir: "view", Description: "views", Files: []string{"ddl.sql"}},
	}
}
func (s *fakeSource) SchemaFiles() []string { return []string{"objects.tsv"} }

func (s *fakeSource) Schemas(context.Context) ([]string, error) {
	s.calls.Add(1)
	return []string{"HR", "WEIRD"}, nil
}

func (s *fakeSource) Objects(_ context.Context, kind string) ([]Object, error) {
	s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail["list:"+kind]; err != nil {
		return nil, err
	}
	return append([]Object(nil), s.objects[kind]...), nil
}

func (s *fakeSource) SchemaFile(_ context.Context, owner, file string, _ int) ([]byte, error) {
	s.calls.Add(1)
	return []byte("objects of " + owner + "\n"), nil
}

func (s *fakeSource) ObjectFile(_ context.Context, kind string, o Object, file string, limit int) ([]byte, error) {
	s.calls.Add(1)
	s.mu.Lock()
	err := s.fail[file]
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if file == "big.txt" {
		return bytes.Repeat([]byte("x"), s.big), nil
	}
	return []byte(fmt.Sprintf("%s %s.%s %s\n", kind, o.Owner, o.Name, file)), nil
}

func newTestFS(src Source) *FS {
	return New(src, Options{
		CacheTTL: time.Minute, QueryTimeout: time.Second, MaxFileBytes: 64 << 20,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func names(infos []os.FileInfo) string {
	var n []string
	for _, i := range infos {
		n = append(n, i.Name())
	}
	sort.Strings(n)
	return strings.Join(n, ",")
}

func TestNameEncodingRoundTrip(t *testing.T) {
	for _, o := range []Object{{"HR", "EMP"}, {"a.b", "c/d"}, {"%", "%2F"}, {"x", "."}, {"SYS", "java/lang/Object"}} {
		enc := objectEntryName(o)
		if strings.Count(enc, ".") != 1 || strings.Contains(enc, "/") {
			t.Errorf("%+v encoded as %q", o, enc)
		}
		got, ok := parseObjectEntryName(enc)
		if !ok || got != o {
			t.Errorf("%+v -> %q -> %+v %v", o, enc, got, ok)
		}
	}
	for _, bad := range []string{".DS_Store", "._HR.EMP", "HR", "HR.", ".x", "A.B.C", "A%2.B", "A%41.B", "A.B%"} {
		if o, ok := parseObjectEntryName(bad); ok {
			t.Errorf("%q should not parse, got %+v", bad, o)
		}
	}
}

func TestTree(t *testing.T) {
	src := newFake()
	f := newTestFS(src)

	root, err := f.ReadDir("")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(root); got != "README.txt,schema,table,view" {
		t.Errorf("root = %s", got)
	}
	tables, err := f.ReadDir("/table")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(tables); got != "HR.DEPT,HR.EMPLOYEES,WEIRD.a%2Fb%2Ec%25d" {
		t.Errorf("tables = %s", got)
	}
	files, err := f.ReadDir("table/WEIRD.a%2Fb%2Ec%25d")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(files); got != "big.txt,ddl.sql,rows.csv" {
		t.Errorf("files = %s", got)
	}
	for _, fi := range files {
		if fi.IsDir() || fi.Mode().Perm() != 0o444 {
			t.Errorf("%s: mode %v", fi.Name(), fi.Mode())
		}
	}

	fh, err := f.Open("/table/WEIRD.a%2Fb%2Ec%25d/ddl.sql")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(fh)
	if string(b) != "table WEIRD.a/b.c%d ddl.sql\n" {
		t.Errorf("content = %q", b)
	}
	info, err := f.Stat("/table/WEIRD.a%2Fb%2Ec%25d/ddl.sql")
	if err != nil || info.Size() != int64(len(b)) {
		t.Errorf("stat size %v err %v", info, err)
	}

	if s, err := f.ReadDir("/schema"); err != nil || names(s) != "HR,WEIRD" {
		t.Errorf("schemas = %v %v", names(s), err)
	}
	if fi, err := f.Stat("/schema/HR/objects.tsv"); err != nil || fi.Size() == 0 {
		t.Errorf("schema file: %v %v", fi, err)
	}
	if fi, err := f.Stat(".metadata_never_index"); err != nil || fi.Size() != 0 {
		t.Errorf("marker: %v %v", fi, err)
	}
	readme, _ := f.Open("README.txt")
	rb, _ := io.ReadAll(readme)
	if !bytes.Contains(rb, []byte("/table/<OWNER>.<NAME>/")) {
		t.Errorf("readme:\n%s", rb)
	}
}

func TestNotExist(t *testing.T) {
	f := newTestFS(newFake())
	for _, p := range []string{
		"nope", "/table/HR.NOPE", "/table/HR.EMPLOYEES/nope.txt", "/view/HR.EMPLOYEES",
		"/table/.DS_Store", "/table/HR.EMPLOYEES/ddl.sql/x", "/schema/NOPE", "/schema/HR/nope",
		"/table/HR.EMPLOYEES/../../etc", "/a/b/c/d",
	} {
		if _, err := f.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: err = %v, want not exist", p, err)
		}
	}
	// Path cleaning keeps ".." inside the tree.
	if fi, err := f.Stat("/table/HR.EMPLOYEES/../HR.DEPT/ddl.sql"); err != nil || fi.Name() != "ddl.sql" {
		t.Errorf("cleaned path: %v %v", fi, err)
	}
}

func TestReadOnly(t *testing.T) {
	f := newTestFS(newFake())
	p := "/table/HR.EMPLOYEES/ddl.sql"
	checks := map[string]error{}
	_, checks["create"] = f.Create("/table/x")
	_, checks["openfile rw"] = f.OpenFile(p, os.O_RDWR, 0)
	_, checks["openfile trunc"] = f.OpenFile(p, os.O_WRONLY|os.O_TRUNC, 0)
	checks["remove"] = f.Remove(p)
	checks["rename"] = f.Rename(p, p+"2")
	checks["mkdir"] = f.MkdirAll("/table/new", 0o755)
	checks["symlink"] = f.Symlink(p, "/l")
	_, checks["tempfile"] = f.TempFile("/", "x")
	for name, err := range checks {
		if !errors.Is(err, billy.ErrReadOnly) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	fh, err := f.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.Write([]byte("x")); !errors.Is(err, billy.ErrReadOnly) {
		t.Errorf("write: %v", err)
	}
	if billy.CapabilityCheck(f, billy.WriteCapability) {
		t.Error("filesystem claims write capability")
	}
	if _, err := f.Open("/table"); err == nil {
		t.Error("opening a directory should fail")
	}
	if _, err := f.ReadDir(p); err == nil {
		t.Error("readdir on a file should fail")
	}
}

func TestCachingAndErrors(t *testing.T) {
	src := newFake()
	src.fail["rows.csv"] = errors.New("ORA-00942: table or view does not exist")
	f := newTestFS(src)

	// A failing file does not break the directory listing.
	files, err := f.ReadDir("/table/HR.EMPLOYEES")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("files = %s", names(files))
	}
	fh, _ := f.Open("/table/HR.EMPLOYEES/rows.csv")
	b, _ := io.ReadAll(fh)
	if !bytes.Contains(b, []byte("ORA-00942")) {
		t.Errorf("error content = %q", b)
	}

	before := src.calls.Load()
	for i := 0; i < 20; i++ {
		if _, err := f.Stat("/table/HR.EMPLOYEES/ddl.sql"); err != nil {
			t.Fatal(err)
		}
	}
	if after := src.calls.Load(); after != before {
		t.Errorf("expected cached stats, got %d extra source calls", after-before)
	}

	// Listing failures surface as errors (no directory to show).
	src.mu.Lock()
	src.fail["list:view"] = errors.New("connection refused")
	src.mu.Unlock()
	if _, err := f.ReadDir("/view"); err == nil {
		t.Error("expected listing error")
	}
}

func TestMaxFileBytes(t *testing.T) {
	src := newFake()
	src.big = 10000
	f := New(src, Options{CacheTTL: time.Minute, QueryTimeout: time.Second, MaxFileBytes: 4096,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	fi, err := f.Stat("/table/HR.DEPT/big.txt")
	if err != nil || fi.Size() != 4096 {
		t.Errorf("size = %v err %v", fi, err)
	}
}

func TestCacheSingleFlightAndBudget(t *testing.T) {
	c := newCache(100)
	var loads atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, _, _ := c.get("k", func() (any, int64, time.Duration, error) {
				loads.Add(1)
				time.Sleep(20 * time.Millisecond)
				return "v", 10, time.Minute, nil
			})
			if v != "v" {
				t.Error("wrong value")
			}
		}()
	}
	wg.Wait()
	if loads.Load() != 1 {
		t.Errorf("loads = %d", loads.Load())
	}

	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	for i := 0; i < 20; i++ {
		now = now.Add(time.Second)
		key := fmt.Sprint("e", i)
		c.get(key, func() (any, int64, time.Duration, error) { return i, 30, time.Hour, nil })
	}
	if c.used > 100 {
		t.Errorf("used %d exceeds budget", c.used)
	}
	c.mu.Lock()
	_, newest := c.entries["e19"]
	_, oldest := c.entries["e0"]
	c.mu.Unlock()
	if !newest || oldest {
		t.Errorf("eviction order wrong: newest=%v oldest=%v", newest, oldest)
	}

	// Errors are not cached; expiry reloads.
	calls := 0
	load := func() (any, int64, time.Duration, error) {
		calls++
		if calls == 1 {
			return nil, 0, 0, errors.New("boom")
		}
		return "ok", 1, time.Second, nil
	}
	if _, _, err := c.get("x", load); err == nil {
		t.Error("expected error")
	}
	if v, _, _ := c.get("x", load); v != "ok" {
		t.Error("expected reload after error")
	}
	now = now.Add(2 * time.Second)
	c.get("x", load)
	if calls != 3 {
		t.Errorf("calls = %d, want reload after expiry", calls)
	}
	// Accounting stays consistent.
	c.mu.Lock()
	var sum int64
	for _, e := range c.entries {
		if e.counted {
			sum += e.size
		}
	}
	c.mu.Unlock()
	if sum != c.used {
		t.Errorf("used=%d, sum=%d", c.used, sum)
	}
}

func TestHandlerStableHandles(t *testing.T) {
	f := newTestFS(newFake())
	h := NewHandler(f)
	a := h.ToHandle(f, []string{"table", "HR.EMP"})
	b := h.ToHandle(f, []string{"table", "HR.EMP"})
	if !bytes.Equal(a, b) {
		t.Error("handles differ for the same path")
	}
	if _, p, err := h.FromHandle(a); err != nil || strings.Join(p, "/") != "table/HR.EMP" {
		t.Errorf("FromHandle: %v %v", p, err)
	}
	// Returned slices are copies.
	_, p, _ := h.FromHandle(a)
	p[0] = "mutated"
	if _, p2, _ := h.FromHandle(a); p2[0] != "table" {
		t.Error("FromHandle leaked internal slice")
	}
	if _, _, err := h.FromHandle([]byte("short")); err == nil {
		t.Error("expected stale for bad handle")
	}
	// Root handle survives a "restart" (new handler).
	root := h.ToHandle(f, nil)
	if _, p, err := NewHandler(f).FromHandle(root); err != nil || len(p) != 0 {
		t.Errorf("root after restart: %v %v", p, err)
	}
}

// TestNFSEndToEnd serves the fake tree over real NFSv3 RPC and reads it back
// with a Go NFS client, covering MOUNT, LOOKUP, READDIRPLUS, READ and the
// read-only error path.
func TestNFSEndToEnd(t *testing.T) {
	src := newFake()
	src.big = 3 << 20 // multi-chunk READs
	f := newTestFS(src)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() { _ = nfs.Serve(l, NewHandler(f)) }()

	c, err := rpc.DialTCP("tcp", l.Addr().String(), false)
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
	defer func() { _ = m.Unmount() }()

	entries, err := target.ReadDirPlus("/table")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		if e.FileName != "." && e.FileName != ".." {
			got = append(got, e.FileName)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "HR.DEPT,HR.EMPLOYEES,WEIRD.a%2Fb%2Ec%25d" {
		t.Errorf("READDIRPLUS /table = %v", got)
	}

	rf, err := target.Open("/table/HR.EMPLOYEES/ddl.sql")
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(rf)
	rf.Close()
	if err != nil || string(b) != "table HR.EMPLOYEES ddl.sql\n" {
		t.Errorf("READ = %q, %v", b, err)
	}

	big, err := target.Open("/table/HR.DEPT/big.txt")
	if err != nil {
		t.Fatal(err)
	}
	bb, err := io.ReadAll(big)
	big.Close()
	if err != nil || len(bb) != src.big {
		t.Errorf("big READ: %d bytes, %v", len(bb), err)
	}

	if _, _, err := target.Lookup("/table/HR.NOPE"); err == nil {
		t.Error("LOOKUP of missing object succeeded")
	}
	if _, err := target.Mkdir("/table/newdir", 0o755); err == nil {
		t.Error("MKDIR succeeded on read-only export")
	}
	if err := target.Remove("/table/HR.EMPLOYEES/ddl.sql"); err == nil {
		t.Error("REMOVE succeeded on read-only export")
	}
	if _, err := target.Create("/README2.txt", 0o644); err == nil {
		t.Error("CREATE succeeded on read-only export")
	}
}
