package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// newTestFS builds the billy layer directly, without the NFS server on top.
func newTestFS(t *testing.T, cfg *Config) *FS {
	t.Helper()
	log := NewLogger(LevelWarn)
	conn := NewConn(cfg, log)
	root, err := resolveRoot(conn, cfg)
	if err != nil {
		conn.Close()
		t.Fatalf("connect: %v", err)
	}
	fsys := NewFS(cfg, conn, log, root)
	t.Cleanup(func() {
		fsys.Close()
		conn.Close()
	})
	return fsys
}

// Fix 2: NFS CREATE is legal on an existing file, so O_CREATE must not reset
// the mode of a file it did not create.
func TestCreateDoesNotClobberExistingMode(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	fsys := newTestFS(t, cfg)

	target := filepath.Join(export, "secret.txt")
	if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := fsys.Create("secret.txt") // O_RDWR|O_CREATE|O_TRUNC, perm 0666
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.Close()

	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600 (create widened an existing file)", fi.Mode().Perm())
	}
}

// Fix 2: a file we really do create still gets the requested mode.
func TestCreateAppliesModeToNewFile(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	fsys := newTestFS(t, cfg)

	f, err := fsys.OpenFile("fresh.txt", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.Close()

	fi, err := os.Stat(filepath.Join(export, "fresh.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, want 0640", fi.Mode().Perm())
	}
}

// Fix 2: MkdirAll on an existing directory is a no-op, mode included.
func TestMkdirAllLeavesExistingDirectoryAlone(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	fsys := newTestFS(t, cfg)

	dir := filepath.Join(export, "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fsys.MkdirAll("d", 0o755); err != nil {
		t.Fatalf("mkdirall: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, want 0700 (mkdirall changed an existing directory)", fi.Mode().Perm())
	}

	if err := fsys.MkdirAll("new/deep", 0o750); err != nil {
		t.Fatalf("mkdirall new: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(export, "new", "deep")); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o750 {
		t.Fatalf("new dir mode = %v, want 0750", fi.Mode().Perm())
	}
}

// Fix 3: a Stat on a symlink must not poison the Lstat cache, or the client
// would see the target instead of the link.
func TestStatDoesNotPoisonLstatCache(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	cfg.AttrCacheTTL = Duration(10 * time.Second)
	fsys := newTestFS(t, cfg)

	if err := os.WriteFile(filepath.Join(export, "target.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(export, "link")); err != nil {
		t.Fatal(err)
	}

	st, err := fsys.Stat("link")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode()&os.ModeSymlink != 0 {
		t.Fatal("Stat should follow the link")
	}

	lst, err := fsys.Lstat("link")
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if lst.Mode()&os.ModeSymlink == 0 {
		t.Fatal("Lstat returned the target's attributes: the caches are still shared")
	}

	// And the reverse order, on a fresh name.
	if err := os.Symlink("target.txt", filepath.Join(export, "link2")); err != nil {
		t.Fatal(err)
	}
	if lst, err := fsys.Lstat("link2"); err != nil || lst.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("lstat link2: %v", err)
	}
	if st, err := fsys.Stat("link2"); err != nil {
		t.Fatalf("stat link2: %v", err)
	} else if st.Mode()&os.ModeSymlink != 0 {
		t.Fatal("Stat returned the link's attributes: the caches are still shared")
	}
}

// Fix 9: link targets are passed through verbatim.
func TestReadlinkPassesTargetsThrough(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	fsys := newTestFS(t, cfg)

	absolute := filepath.Join(export, "inside.txt")
	if err := os.Symlink(absolute, filepath.Join(export, "abs")); err != nil {
		t.Fatal(err)
	}
	got, err := fsys.Readlink("abs")
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if got != absolute {
		t.Fatalf("readlink = %q, want %q", got, absolute)
	}
}

// Fix 8: a write invalidates the file's attributes but not the listing of the
// directory holding it.
func TestWriteKeepsParentDirectoryCache(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	cfg.AttrCacheTTL = Duration(10 * time.Second)
	cfg.DirCacheTTL = Duration(10 * time.Second)
	fsys := newTestFS(t, cfg)

	if err := os.WriteFile(filepath.Join(export, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.ReadDir("."); err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if _, ok := fsys.attrs.getDir(fsys.root); !ok {
		t.Fatal("directory listing was not cached")
	}

	f, err := fsys.OpenFile("a.txt", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()

	if _, ok := fsys.attrs.getDir(fsys.root); !ok {
		t.Fatal("a write dropped the parent directory listing")
	}
	if _, ok := fsys.attrs.getAttr(fsys.Join(fsys.root, "a.txt"), false); ok {
		t.Fatal("a write left stale attributes cached for the file itself")
	}

	// Creating a name must still drop the listing.
	nf, err := fsys.Create("b.txt")
	if err != nil {
		t.Fatal(err)
	}
	nf.Close()
	if _, ok := fsys.attrs.getDir(fsys.root); ok {
		t.Fatal("creating a file left a stale directory listing")
	}
}

// Fix 1: rename replaces the destination, which is what NFS RENAME requires.
func TestRenameReplacesDestination(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	fsys := newTestFS(t, cfg)

	if err := os.WriteFile(filepath.Join(export, "src"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(export, "dst"), []byte("destination"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Rename("src", "dst"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(export, "dst"))
	if err != nil || string(got) != "source" {
		t.Fatalf("dst = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(export, "src")); !os.IsNotExist(err) {
		t.Fatalf("src still present: %v", err)
	}
}

// Fix 1: the destructive fallback runs only for "operation unsupported".
func TestIsUnsupported(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("some failure"), false},
		{os.ErrPermission, false},
		{&sftp.StatusError{Code: 3 /* permission denied */}, false},
		{&sftp.StatusError{Code: 4 /* failure */}, false},
		{&sftp.StatusError{Code: 8 /* op unsupported */}, true},
		{sftp.ErrSSHFxOpUnsupported, true},
	}
	for _, c := range cases {
		if got := isUnsupported(c.err); got != c.want {
			t.Errorf("isUnsupported(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// Fix 4: after a failed dial, further attempts fail fast instead of each
// paying the full connect timeout.
func TestDialFailureIsCachedBriefly(t *testing.T) {
	// A listener that accepts and then says nothing, so the SSH handshake
	// hangs until the configured timeout.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()

	cfg := DefaultConfig()
	cfg.Host, cfg.Port = "127.0.0.1", l.Addr().(*net.TCPAddr).Port
	cfg.User = "nobody"
	cfg.UseAgent = false
	cfg.Password = "x"
	cfg.InsecureHostKey = true
	cfg.ConnectTimeout = Duration(600 * time.Millisecond)

	conn := NewConn(cfg, NewLogger(LevelError))
	defer conn.Close()

	start := time.Now()
	if _, _, err := conn.Client(); err == nil {
		t.Fatal("expected the first dial to fail")
	}
	first := time.Since(start)
	if first < 300*time.Millisecond {
		t.Skipf("dial failed too quickly (%v) to measure backoff", first)
	}

	start = time.Now()
	if _, _, err := conn.Client(); err == nil {
		t.Fatal("expected the second dial to fail")
	}
	if second := time.Since(start); second > 100*time.Millisecond {
		t.Fatalf("second attempt took %v; the failure was not cached", second)
	}
}

// Fix 5: secrets never appear in -print-config output.
func TestSecretsAreRedacted(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Host, cfg.User = "example", "me"
	cfg.Password = "hunter2"
	cfg.IdentityPassphrase = "correct horse"

	out := cfg.JSON()
	if strings.Contains(out, "hunter2") || strings.Contains(out, "correct horse") {
		t.Fatalf("secrets leaked into JSON output:\n%s", out)
	}
	if !strings.Contains(out, redacted) {
		t.Fatal("expected a redaction marker")
	}
	// The redacted output must not be silently accepted back as input.
	var round map[string]interface{}
	if err := json.Unmarshal([]byte(out), &round); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig([]string{"-config", path}, os.Stderr); err == nil {
		t.Fatal("loading a config with a redacted secret should fail")
	}
}

// Fix 7: -i replaces the config file's list rather than appending to it.
func TestIdentityFlagReplacesConfigValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")
	body := `{"host":"h","user":"u","identity_files":["/from/file"],"known_hosts_file":"~/.ssh/known_hosts","use_ssh_config":false}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	load := func(args ...string) *Config {
		t.Helper()
		got, err := LoadConfig(args, os.Stderr)
		if err != nil {
			t.Fatalf("load %v: %v", args, err)
		}
		if len(got) != 1 {
			t.Fatalf("expected a single mount, got %d", len(got))
		}
		return got[0]
	}

	cfg := load("-config", path)
	if len(cfg.IdentityFiles) != 1 || cfg.IdentityFiles[0] != "/from/file" {
		t.Fatalf("config file value lost: %v", cfg.IdentityFiles)
	}

	cfg = load("-config", path, "-i", "/from/flag")
	if len(cfg.IdentityFiles) != 1 || cfg.IdentityFiles[0] != "/from/flag" {
		t.Fatalf("IdentityFiles = %v, want exactly [/from/flag]", cfg.IdentityFiles)
	}

	cfg = load("-config", path, "-i", "/a", "-i", "/b")
	if len(cfg.IdentityFiles) != 2 {
		t.Fatalf("repeated -i should accumulate: %v", cfg.IdentityFiles)
	}
}

// Fix 6: closing a pooled handle must not happen under the cache lock. This
// exercises eviction while other handles are being acquired.
func TestFileCacheEvictionUnderLoad(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	cfg.FileCacheMax = 2
	fsys := newTestFS(t, cfg)

	for i := 0; i < 8; i++ {
		name := filepath.Join(export, string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 4)
	for w := 0; w < 4; w++ {
		go func() {
			for i := 0; i < 8; i++ {
				f, err := fsys.Open(string(rune('a'+i)) + ".txt")
				if err != nil {
					done <- err
					return
				}
				buf := make([]byte, 1)
				if _, err := f.Read(buf); err != nil {
					f.Close()
					done <- err
					return
				}
				f.Close()
			}
			done <- nil
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent access: %v", err)
		}
	}
	if got := len(fsys.files.entries); got > cfg.FileCacheMax {
		t.Fatalf("pool holds %d handles, limit is %d", got, cfg.FileCacheMax)
	}
}
