package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestBackslashIsAnOrdinaryCharacter(t *testing.T) {
	fsys := &FS{root: "/srv/export"}
	if got := fsys.resolve(`dir/a\b`); got != `/srv/export/dir/a\b` {
		t.Fatalf(`resolve("dir/a\b") = %q; a backslash must not become a separator`, got)
	}
	if got := fsys.resolve(`..\..\etc`); !strings.HasPrefix(got, "/srv/export/") {
		t.Fatalf("resolve escaped the root: %q", got)
	}
}

// A remove whose reply was lost with the connection is reported as done when
// the retry finds the file already gone.
func TestRetryReconcilesLostReply(t *testing.T) {
	d := startStallingSSHD(t)
	cfg := stallConfig(t, d.addr)
	log := NewLogger(LevelPanic)
	conn := NewConn(cfg, log)
	defer conn.Close()
	fsys := NewFS(cfg, conn, log, "/")
	defer fsys.Close()

	f, err := fsys.Create("/victim")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.Close()

	first := true
	remove := func(c *sftp.Client) error {
		err := c.Remove("/victim")
		if first {
			first = false
			return io.ErrUnexpectedEOF // done, but the reply "got lost"
		}
		return err
	}
	reconcile := func(_ *sftp.Client, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := fsys.doRetry("remove /victim", remove, reconcile); err != nil {
		t.Fatalf("remove reported %v although the first attempt succeeded", err)
	}
	// Without reconciliation the same sequence is an error.
	first = true
	if err := fsys.doRetry("remove /victim", remove, nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("without reconcile: got %v, want not-exist", err)
	}
}

func TestStatVFSAnswerIsCached(t *testing.T) {
	d := startStallingSSHD(t)
	cfg := stallConfig(t, d.addr)
	log := NewLogger(LevelPanic)
	conn := NewConn(cfg, log)
	defer conn.Close()
	fsys := NewFS(cfg, conn, log, "/")
	defer fsys.Close()

	_, err1 := fsys.StatVFS()
	if err1 == nil {
		t.Skip("test server supports statvfs")
	}
	// The answer is reused rather than asked for again: kill the session,
	// and a second call that went to the server would see a different error.
	if _, gen, ok := conn.current(); ok {
		conn.Invalidate(gen)
	}
	if _, err2 := fsys.StatVFS(); err2 != err1 {
		t.Fatalf("second call: got %v, want the cached %v", err2, err1)
	}
}

func TestHostKeyAlgorithmsFollowKnownHosts(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	kh := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{knownhosts.Normalize("example.org:2222")}, signer.PublicKey())
	if err := os.WriteFile(kh, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cb, err := knownhosts.New(kh)
	if err != nil {
		t.Fatal(err)
	}
	got := knownHostAlgorithms(cb, "example.org:2222")
	if len(got) != 1 || got[0] != ssh.KeyAlgoED25519 {
		t.Fatalf("algorithms = %v, want [%s]", got, ssh.KeyAlgoED25519)
	}
	if got := knownHostAlgorithms(cb, "unknown.org:22"); got != nil {
		t.Fatalf("unknown host: algorithms = %v, want none (no preference)", got)
	}
}

func TestPrivilegedListenerRefusesUnprivilegedPorts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pl := &privilegedListener{Listener: ln, log: NewLogger(LevelPanic)}
	defer pl.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := pl.Accept(); err == nil {
			accepted <- c
		}
	}()

	c, err := net.Dial("tcp", ln.Addr().String()) // an ordinary, unprivileged port
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection from an unprivileged port was not closed")
	}
	select {
	case <-accepted:
		t.Fatal("connection from an unprivileged port was handed to the NFS server")
	default:
	}
}

func TestMountOptionsForPrivilegedPort(t *testing.T) {
	got := mountOptions("vers=3,tcp,noresvport,actimeo=1", 2049, true)
	if strings.Contains(got, "noresvport") || !strings.Contains(got, ",resvport") {
		t.Fatalf("options = %q, want noresvport replaced by resvport", got)
	}
	if got := mountOptions("vers=3,noresvport", 2049, false); !strings.Contains(got, "noresvport") {
		t.Fatalf("options = %q, want them left alone", got)
	}
}

func TestPrivilegedPortDefaultsOnForMacOS(t *testing.T) {
	cfg := DefaultConfig()
	if want := runtime.GOOS == "darwin"; cfg.RequirePrivilegedPort != want {
		t.Fatalf("RequirePrivilegedPort = %v on %s, want %v", cfg.RequirePrivilegedPort, runtime.GOOS, want)
	}
	// The macOS defaults must not fight the check.
	if strings.Contains(defaultMountOptionsDarwin, "noresvport") || !strings.Contains(defaultMountOptionsDarwin, "resvport") {
		t.Fatalf("darwin mount options %q should use resvport", defaultMountOptionsDarwin)
	}
}

// An open that fails because the session died behind our back retries on a
// fresh session, not on the same dead one.
func TestOpenRetriesOnAFreshSession(t *testing.T) {
	d := startStallingSSHD(t)
	cfg := stallConfig(t, d.addr)
	log := NewLogger(LevelPanic)
	conn := NewConn(cfg, log)
	defer conn.Close()
	fsys := NewFS(cfg, conn, log, "/")
	defer fsys.Close()

	f, err := fsys.Create("/file")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.Close()
	fsys.files.dropAll()

	// Kill the sftp session only: the ssh transport stays up, so nothing
	// but the failing request itself can notice.
	cl, gen, ok := conn.current()
	if !ok {
		t.Fatal("no session")
	}
	// Close waits for the server to close the channel too, which this one
	// does not; that is why teardown never waits on it either.
	go cl.Close()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := cl.Getwd(); err != nil {
			if !isConnError(err) {
				t.Fatalf("a request on the closed session failed with %v, not a connection error", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the closed session still answers")
		}
	}

	// The test server gives every session a fresh in-memory filesystem, so
	// on the new session the file is gone: "does not exist" is the answer
	// of a live server, a connection error that of the dead one.
	f, err = fsys.Open("/file")
	if err == nil {
		f.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open after the session died: %v", err)
	}
	if _, gen2, _ := conn.current(); gen2 == gen {
		t.Fatal("still on the dead session")
	}
}

func TestStatVFSErrorNamesTheExtension(t *testing.T) {
	if !strings.Contains(errNoStatVFS.Error(), "statvfs@openssh.com") {
		t.Fatalf("errNoStatVFS = %q", errNoStatVFS)
	}
}

func TestAbortPromptWithoutPrompt(t *testing.T) {
	if abortPrompt() {
		t.Fatal("abortPrompt reported a prompt although none is waiting")
	}
}

// A failed call gives the pooled handle back at once: go-nfs does not close
// the file after a failed WRITE or SETATTR, so waiting for Close would leak
// the reference (and the remote handle) for the life of the session.
func TestFailedCallReleasesTheHandle(t *testing.T) {
	d := startStallingSSHD(t)
	cfg := stallConfig(t, d.addr)
	log := NewLogger(LevelPanic)
	conn := NewConn(cfg, log)
	defer conn.Close()
	fsys := NewFS(cfg, conn, log, "/")
	defer fsys.Close()

	f, err := fsys.Create("/file")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.Close()

	bf, err := fsys.OpenFile("/file", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	file := bf.(*File)
	if !file.entry.pooled {
		t.Fatal("expected a pooled handle")
	}
	if _, err := file.Seek(-1, 0); err == nil {
		t.Fatal("a negative seek should fail")
	}
	fsys.files.mu.Lock()
	refs := file.entry.refs
	fsys.files.mu.Unlock()
	if refs != 0 {
		t.Fatalf("handle still referenced %d times after a failed call", refs)
	}
	if _, err := file.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("write after a failure: got %v, want os.ErrClosed", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close after a failure: %v", err)
	}
	fsys.files.mu.Lock()
	refs = file.entry.refs
	fsys.files.mu.Unlock()
	if refs != 0 {
		t.Fatalf("close after a failure released the handle again (refs %d)", refs)
	}
}

// ProxyCommand tokens are expanded once: a literal %% must survive.
func TestProxyCommandTokensExpandOnce(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Host, cfg.Port, cfg.User = "example.net", 2200, "u"
	cfg.UseSSHConfig = false
	cfg.ProxyCommand = "printf %%h; nc %h %p"
	if err := cfg.applySSHConfig(); err != nil {
		t.Fatal(err)
	}
	if want := "printf %h; nc example.net 2200"; cfg.ProxyCommand != want {
		t.Fatalf("ProxyCommand = %q, want %q", cfg.ProxyCommand, want)
	}
}

// go-nfs truncates through O_WRONLY|O_EXCL; without O_CREATE the O_EXCL is
// dropped, so the request is an ordinary pooled open.
func TestTruncateOpenDropsMeaninglessExcl(t *testing.T) {
	d := startStallingSSHD(t)
	cfg := stallConfig(t, d.addr)
	log := NewLogger(LevelPanic)
	conn := NewConn(cfg, log)
	defer conn.Close()
	fsys := NewFS(cfg, conn, log, "/")
	defer fsys.Close()

	f, err := fsys.Create("/file")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.Write([]byte("hello"))
	f.Close()

	bf, err := fsys.OpenFile("/file", os.O_WRONLY|os.O_EXCL, 0)
	if err != nil {
		t.Fatalf("open for truncate: %v", err)
	}
	if !bf.(*File).entry.pooled {
		t.Fatal("the truncate open was kept out of the pool")
	}
	if err := bf.Truncate(2); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	bf.Close()
	fi, err := fsys.Stat("/file")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != 2 {
		t.Fatalf("size %d after truncate, want 2", fi.Size())
	}

	// With O_CREATE, O_EXCL keeps its meaning.
	if _, err := fsys.OpenFile("/file", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644); err == nil {
		t.Fatal("exclusive create of an existing file succeeded")
	}
}

func TestSplitTarget(t *testing.T) {
	cases := []struct {
		in, user, host, path string
		hasPath              bool
	}{
		{"host", "", "host", "", false},
		{"host:", "", "host", "", true},
		{"bob@host:/srv", "bob", "host", "/srv", true},
		{"jdoe@corp.example@winhost:/c/x", "jdoe@corp.example", "winhost", "/c/x", true},
		{"bob@host:/odd@name:x", "bob", "host", "/odd@name:x", true},
		{"[::1]:/srv", "", "::1", "/srv", true},
		{"bob@[fe80::1]", "bob", "fe80::1", "", false},
		{"host:relative/dir", "", "host", "relative/dir", true},
	}
	for _, c := range cases {
		user, host, path, hasPath, err := splitTarget(c.in)
		if err != nil || user != c.user || host != c.host || path != c.path || hasPath != c.hasPath {
			t.Errorf("splitTarget(%q) = %q, %q, %q, %v, %v; want %q, %q, %q, %v",
				c.in, user, host, path, hasPath, err, c.user, c.host, c.path, c.hasPath)
		}
	}
	if _, _, _, _, err := splitTarget("[::1"); err == nil {
		t.Error("unbalanced bracket accepted")
	}
}

func TestVersionNeedsNoConfiguration(t *testing.T) {
	mounts, err := LoadConfig([]string{"-version", "-use-ssh-config=false"}, io.Discard)
	if err != nil || !mounts[0].showVersion {
		t.Fatalf("-version: %v", err)
	}
}

func TestPrintConfigSkipsMountPointChecks(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-there")
	_, err := LoadConfig([]string{"-print-config", "-use-ssh-config=false", "-host", "h", "-mount", missing}, io.Discard)
	if err != nil {
		t.Fatalf("-print-config with a missing mount point: %v", err)
	}
	if _, err := LoadConfig([]string{"-use-ssh-config=false", "-host", "h", "-mount", missing}, io.Discard); err == nil {
		t.Fatal("a missing mount point was accepted for a real run")
	}
}

// example.multi-host.json with example.multi-host.ssh_config resolves every
// mount from ssh config, the jump host included. Keeps the example honest.
func TestMultiHostExampleResolvesFromSSHConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id_build", "id_ops", "id_db", "id_nas", "known_hosts"} {
		if err := os.WriteFile(filepath.Join(ssh, k), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mounts, err := LoadConfig([]string{"-print-config",
		"-F", "example.multi-host.ssh_config", "-config", "example.multi-host.json"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 3 {
		t.Fatalf("got %d mounts, want 3", len(mounts))
	}
	key := func(name string) string { return filepath.Join(ssh, name) }
	want := []struct {
		name, host, user, key string
		port                  int
		connect, keepalive    time.Duration
	}{
		{"build", "build.internal.example.com", "builder", key("id_build"), 2222, 15 * time.Second, 30 * time.Second},
		{"db-logs", "10.20.0.15", "postgres", key("id_db"), 22, 15 * time.Second, 30 * time.Second},
		{"nas", "nas.home.arpa", "backup", key("id_nas"), 2200, 30 * time.Second, 15 * time.Second},
	}
	for i, w := range want {
		m := mounts[i]
		if m.Name != w.name || m.Host != w.host || m.User != w.user || m.Port != w.port ||
			len(m.IdentityFiles) != 1 || m.IdentityFiles[0] != w.key ||
			m.ConnectTimeout.D() != w.connect || m.KeepAlive.D() != w.keepalive {
			t.Errorf("mount %d: got name=%s host=%s user=%s port=%d keys=%v connect=%s keepalive=%s; want %+v",
				i, m.Name, m.Host, m.User, m.Port, m.IdentityFiles, m.ConnectTimeout, m.KeepAlive, w)
		}
		if m.KnownHostsFile != key("known_hosts") {
			t.Errorf("mount %d: known_hosts_file = %s", i, m.KnownHostsFile)
		}
	}
	hops := mounts[1].jumpHops
	if len(hops) != 1 || hops[0].Host != "bastion.example.com" || hops[0].User != "ops" ||
		len(hops[0].IdentityFiles) != 1 || hops[0].IdentityFiles[0] != key("id_ops") {
		t.Errorf("db jump hops = %+v; want ops@bastion.example.com with id_ops", hops)
	}
	if len(mounts[0].jumpHops) != 0 || len(mounts[2].jumpHops) != 0 {
		t.Error("a jump host leaked into a mount that has none")
	}
}

// An error that merely names a file containing "EOF" (or another phrase that
// a lost connection uses) is not a lost connection.
func TestIsConnErrorIgnoresFileNames(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&os.PathError{Op: "remove", Path: "/srv/GEOFF.txt", Err: os.ErrNotExist}, false},
		{&os.PathError{Op: "stat", Path: "/srv/EOF.log", Err: os.ErrNotExist}, false},
		{&os.PathError{Op: "open", Path: "/srv/broken pipe/x", Err: os.ErrPermission}, false},
		{&os.PathError{Op: "open", Path: "/srv/session closed", Err: &sftp.StatusError{Code: 4}}, false},
		{io.EOF, true},
		{io.ErrUnexpectedEOF, true},
		{&os.PathError{Op: "read", Path: "/srv/GEOFF.txt", Err: io.EOF}, true},
		{sftp.ErrSSHFxConnectionLost, true},
		{fmt.Errorf("failed to send packet: %w", io.EOF), true},
		{errors.New("failed to send packet: EOF"), true},
		{errors.New("write: broken pipe"), true},
		{errors.New("GEOFF said no"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isConnError(c.err); got != c.want {
			t.Errorf("isConnError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// Removing a missing file whose name contains "EOF" fails normally and
// leaves the session alone.
func TestFailedRemoveOfEOFNameKeepsSession(t *testing.T) {
	d := startStallingSSHD(t)
	cfg := stallConfig(t, d.addr)
	log := NewLogger(LevelPanic)
	conn := NewConn(cfg, log)
	defer conn.Close()
	fsys := NewFS(cfg, conn, log, "/")
	defer fsys.Close()

	if _, err := fsys.Stat("/"); err != nil {
		t.Fatal(err)
	}
	_, gen, _ := conn.current()
	if err := fsys.Remove("/GEOFF-EOF.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove of a missing file: got %v, want not-exist", err)
	}
	if _, gen2, ok := conn.current(); !ok || gen2 != gen {
		t.Fatal("a plain not-exist error dropped the session")
	}
}

func writeRemote(t *testing.T, c *sftp.Client, name, data string) {
	t.Helper()
	f, err := c.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func readRemote(t *testing.T, c *sftp.Client, name string) string {
	t.Helper()
	f, err := c.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func noLeftovers(t *testing.T, c *sftp.Client, dir string) {
	t.Helper()
	list, err := c.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, fi := range list {
		if strings.HasPrefix(fi.Name(), ".sshnfs-") {
			t.Errorf("left behind: %s", fi.Name())
		}
	}
}

// The plain-SFTP rename fallback replaces an existing destination, but never
// loses it when the rename itself cannot happen.
func TestRenameFallback(t *testing.T) {
	d := startStallingSSHD(t)
	cfg := stallConfig(t, d.addr)
	log := NewLogger(LevelPanic)
	conn := NewConn(cfg, log)
	defer conn.Close()
	fsys := NewFS(cfg, conn, log, "/")
	defer fsys.Close()
	c, _, err := conn.Client()
	if err != nil {
		t.Fatal(err)
	}

	// Replaces, as NFS RENAME must (the test server refuses a plain rename
	// onto an existing name, like any server without posix-rename).
	writeRemote(t, c, "/a", "new")
	writeRemote(t, c, "/b", "old")
	if err := replace(t, fsys, c, "/a", "/b"); err != nil {
		t.Fatalf("replacing rename: %v", err)
	}
	if got := readRemote(t, c, "/b"); got != "new" {
		t.Fatalf("/b = %q after rename, want new", got)
	}
	if _, err := c.Lstat("/a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("/a still there: %v", err)
	}
	noLeftovers(t, c, "/")

	// A missing source must not cost the destination.
	writeRemote(t, c, "/keep", "precious")
	if err := replace(t, fsys, c, "/missing", "/keep"); err == nil {
		t.Fatal("rename of a missing source succeeded")
	}
	if got := readRemote(t, c, "/keep"); got != "precious" {
		t.Fatalf("destination damaged by a failed rename: %q", got)
	}

	// A directory does not replace a file.
	if err := c.Mkdir("/dir"); err != nil {
		t.Fatal(err)
	}
	if err := replace(t, fsys, c, "/dir", "/keep"); err == nil {
		t.Fatal("a directory replaced a file")
	}
	if got := readRemote(t, c, "/keep"); got != "precious" {
		t.Fatalf("file damaged by a refused rename: %q", got)
	}
	noLeftovers(t, c, "/")

	// A dangling symlink in the way is replaced like anything else.
	if err := c.Symlink("/nowhere", "/link"); err != nil {
		t.Skipf("test server cannot make symlinks: %v", err)
	}
	writeRemote(t, c, "/src", "data")
	if err := replace(t, fsys, c, "/src", "/link"); err != nil {
		t.Fatalf("rename onto a dangling symlink: %v", err)
	}
	if got := readRemote(t, c, "/link"); got != "data" {
		t.Fatalf("/link = %q, want data", got)
	}
	noLeftovers(t, c, "/")
}

// Renaming a directory forgets pooled handles and cached attributes for
// everything under it, so its old names cannot reach the moved files.
func TestRenameDirectoryDropsChildren(t *testing.T) {
	d := startStallingSSHD(t)
	cfg := stallConfig(t, d.addr)
	log := NewLogger(LevelPanic)
	conn := NewConn(cfg, log)
	defer conn.Close()
	fsys := NewFS(cfg, conn, log, "/")
	defer fsys.Close()

	if err := fsys.MkdirAll("/A", 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := fsys.Create("/A/f")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	f, err = fsys.OpenFile("/A/f", os.O_RDWR, 0) // leaves a pooled handle
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := fsys.Stat("/A/f"); err != nil { // and a cached stat
		t.Fatal(err)
	}

	if err := fsys.Rename("/A", "/B"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	fsys.files.mu.Lock()
	for _, e := range fsys.files.entries {
		if underPath(e.path, "/A") {
			t.Errorf("pooled handle for %s survived the rename of /A", e.path)
		}
	}
	fsys.files.mu.Unlock()
	if _, ok := fsys.attrs.getAttr("/A/f", false); ok {
		t.Error("cached attributes for /A/f survived the rename of /A")
	}
	if _, err := fsys.Stat("/A/f"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat /A/f after the rename: got %v, want not-exist", err)
	}
}

func TestUnderPath(t *testing.T) {
	cases := []struct {
		p, root string
		want    bool
	}{
		{"/A", "/A", true}, {"/A/f", "/A", true}, {"/AB/f", "/A", false},
		{"/B", "/A", false}, {"/x", "/", true},
	}
	for _, c := range cases {
		if got := underPath(c.p, c.root); got != c.want {
			t.Errorf("underPath(%q, %q) = %v", c.p, c.root, got)
		}
	}
}

// An explicit proxy_command keeps ssh config's ProxyJump from applying.
func TestExplicitProxyCommandBeatsSSHConfigJump(t *testing.T) {
	sshcfg := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(sshcfg, []byte("Host inner\n  HostName 10.0.0.1\n  ProxyJump bastion\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mounts, err := LoadConfig([]string{"-print-config", "-insecure", "-user", "u", "-F", sshcfg,
		"-proxy-command", "nc %h %p", "inner:/srv"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	m := mounts[0]
	if m.ProxyCommand != "nc 10.0.0.1 22" || len(m.jumpHops) != 0 {
		t.Fatalf("proxy_command %q, %d jump hops; want the command and no hops", m.ProxyCommand, len(m.jumpHops))
	}
	// Without it, ssh config's ProxyJump applies as before.
	mounts, err = LoadConfig([]string{"-print-config", "-insecure", "-user", "u", "-F", sshcfg, "inner:/srv"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts[0].jumpHops) != 1 {
		t.Fatalf("got %d jump hops, want ssh config's one", len(mounts[0].jumpHops))
	}
}

func TestNFSSourceBracketsIPv6(t *testing.T) {
	if got := nfsSource("::1"); got != "[::1]:/" {
		t.Errorf("nfsSource(::1) = %q", got)
	}
	if got := nfsSource("127.0.0.1"); got != "127.0.0.1:/" {
		t.Errorf("nfsSource(127.0.0.1) = %q", got)
	}
}

type countingCloser struct{ n atomic.Int32 }

func (c *countingCloser) Close() error { c.n.Add(1); return nil }

// When the timer fires, the connection is closed, so success must not be
// reported even if fn returns nil afterwards.
func TestWithTimeoutReportsAFiredTimer(t *testing.T) {
	cc := &countingCloser{}
	err := withTimeout(cc, 10*time.Millisecond, func() error {
		time.Sleep(100 * time.Millisecond)
		return nil
	})
	if err == nil || cc.n.Load() == 0 {
		t.Fatalf("err = %v, closes = %d; want a timeout and a closed connection", err, cc.n.Load())
	}
	cc = &countingCloser{}
	if err := withTimeout(cc, time.Second, func() error { return nil }); err != nil || cc.n.Load() != 0 {
		t.Fatalf("fast fn: err = %v, closes = %d", err, cc.n.Load())
	}
}

func TestLoggerWithLevel(t *testing.T) {
	base := NewLogger(LevelInfo)
	if l := base.With("m").WithLevel(LevelDebug); l.level != LevelDebug || l.prefix != "m" || base.level != LevelInfo {
		t.Fatalf("WithLevel: got level %v prefix %q, base %v", l.level, l.prefix, base.level)
	}
}

// A timeout is never a lost connection, whatever its path says, and every
// branch of a joined error is looked at on its own.
func TestIsConnErrorTimeoutsAndJoins(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&opTimeoutError{op: "stat /srv/EOF.log", why: "no reply from the server within 30s"}, false},
		{fmt.Errorf("x: %w", &opTimeoutError{op: "open /srv/broken pipe", why: "still running after 10m"}), false},
		{errors.Join(&os.PathError{Op: "stat", Path: "/srv/EOF", Err: os.ErrNotExist}, errors.New("other")), false},
		{errors.Join(errors.New("first"), errors.New("write: broken pipe")), true},
		{errors.Join(errors.New("first"), io.EOF), true},
	}
	for _, c := range cases {
		if got := isConnError(c.err); got != c.want {
			t.Errorf("isConnError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// replace runs the fallback once, with a fresh parking name, and the
// cleanup Rename would start after it. It checks that the fallback never
// returns with the destination still marked as parked unless the connection
// failed, and marks it replaced only when it succeeds.
func replace(t *testing.T, fsys *FS, c *sftp.Client, from, to string) error {
	t.Helper()
	var state atomic.Int32
	aside := asideName(to)
	err := fsys.renameReplacing(c, from, to, aside, &state)
	switch st := state.Load(); {
	case st == renameParked && !isConnError(err):
		t.Errorf("rename %s to %s: returned %v with the destination still marked as parked", from, to, err)
	case st >= renameReplacedFile && err != nil:
		t.Errorf("rename %s to %s: returned %v but marked the destination replaced", from, to, err)
	case st >= renameReplacedFile:
		fsys.removeReplaced(from, to, aside, st == renameReplacedDir)
	}
	return err
}

func sftpFixture(t *testing.T) (*FS, *sftp.Client) {
	t.Helper()
	d := startStallingSSHD(t)
	cfg := stallConfig(t, d.addr)
	log := NewLogger(LevelPanic)
	conn := NewConn(cfg, log)
	t.Cleanup(func() { conn.Close() })
	fsys := NewFS(cfg, conn, log, "/")
	t.Cleanup(fsys.Close)
	c, _, err := conn.Client()
	if err != nil {
		t.Fatal(err)
	}
	return fsys, c
}

func exists(t *testing.T, c *sftp.Client, p string) bool {
	t.Helper()
	_, err := lookupEntry(c, p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lookup %s: %v", p, err)
	}
	return err == nil
}

// Renaming a name to itself changes nothing, as in POSIX.
func TestRenameOntoItself(t *testing.T) {
	fsys, c := sftpFixture(t)
	writeRemote(t, c, "/same", "data")
	if err := replace(t, fsys, c, "/same", "/same"); err != nil {
		t.Fatalf("rename onto itself: %v", err)
	}
	if got := readRemote(t, c, "/same"); got != "data" {
		t.Fatalf("/same = %q", got)
	}
	noLeftovers(t, c, "/")
	if err := replace(t, fsys, c, "/nothing", "/nothing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rename of a missing name onto itself: got %v, want not-exist", err)
	}
}

// The parking name is short whatever the destination's name, so it cannot
// exceed NAME_MAX.
func TestAsideNameIsShort(t *testing.T) {
	long := "/dir/" + strings.Repeat("x", 250)
	aside := asideName(long)
	if n := len(path.Base(aside)); n > 32 {
		t.Fatalf("aside name is %d bytes", n)
	}
	if path.Dir(aside) != "/dir" {
		t.Fatalf("aside %s is not next to the destination", aside)
	}
}

// After a replacing rename was cut off, settleAside puts the destination
// back if its name is empty, and never deletes it if the name is taken.
func TestSettleAside(t *testing.T) {
	fsys, c := sftpFixture(t)

	// Parked, and the rename never happened: put it back.
	writeRemote(t, c, "/keep", "precious")
	aside := asideName("/keep")
	if err := c.Rename("/keep", aside); err != nil {
		t.Fatal(err)
	}
	fsys.settleAside("/src", "/keep", aside)
	if got := readRemote(t, c, "/keep"); got != "precious" || exists(t, c, aside) {
		t.Fatalf("not restored: %q, aside there=%v", got, exists(t, c, aside))
	}

	// The name is taken (by the rename, or by someone else): the parked
	// file stays where it is.
	writeRemote(t, c, "/old", "replaced")
	writeRemote(t, c, "/dst", "renamed")
	aside = asideName("/dst")
	if err := c.Rename("/old", aside); err != nil {
		t.Fatal(err)
	}
	fsys.settleAside("/src", "/dst", aside)
	if got := readRemote(t, c, "/dst"); got != "renamed" {
		t.Fatalf("/dst = %q", got)
	}
	if got := readRemote(t, c, aside); got != "replaced" {
		t.Fatalf("the parked file was touched: %q", got)
	}
	if err := c.Remove(aside); err != nil {
		t.Fatal(err)
	}

	// Nothing parked: nothing happens.
	fsys.settleAside("/src", "/dst", asideName("/dst"))
	if got := readRemote(t, c, "/dst"); got != "renamed" {
		t.Fatalf("/dst = %q", got)
	}
	noLeftovers(t, c, "/")
}

// lookupEntry reports a name absent only when the server says so; a failure
// to ask is an error, so nothing acts on a parked file it merely failed to
// see.
func TestLookupEntryErrorIsNotAbsence(t *testing.T) {
	_, c := sftpFixture(t)
	writeRemote(t, c, "/here", "x")
	if fi, err := lookupEntry(c, "/here"); err != nil || fi.Name() != "here" {
		t.Fatalf("lookup /here: %v, %v", fi, err)
	}
	if _, err := lookupEntry(c, "/absent"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lookup /absent: got %v, want not-exist", err)
	}
	go c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := c.Lstat("/here"); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the client never closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := lookupEntry(c, "/absent"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lookup on a closed client: got %v, want an error other than not-exist", err)
	}
}

// Only a name the cache knows to be a regular file skips the subtree walk:
// a directory, a symlink (which may lead to one) or an unknown name does
// not.
func TestKnownRegular(t *testing.T) {
	fsys, c := sftpFixture(t)
	writeRemote(t, c, "/file", "x")
	if err := c.Mkdir("/dir"); err != nil {
		t.Fatal(err)
	}
	if fsys.knownRegular("/file") {
		t.Fatal("nothing cached yet, but /file was judged a regular file")
	}
	fsys.Lstat("/file")
	fsys.Lstat("/dir")
	if !fsys.knownRegular("/file") || fsys.knownRegular("/dir") {
		t.Fatalf("knownRegular: /file=%v /dir=%v", fsys.knownRegular("/file"), fsys.knownRegular("/dir"))
	}
	if err := c.Symlink("/dir", "/link"); err != nil {
		t.Skipf("test server cannot make symlinks: %v", err)
	}
	if fi, err := fsys.Lstat("/link"); err == nil && fi.Mode()&os.ModeSymlink != 0 && fsys.knownRegular("/link") {
		t.Fatal("a symlink was judged a regular file")
	}
}

func queued(fsys *FS) int {
	fsys.unsettled.mu.Lock()
	defer fsys.unsettled.mu.Unlock()
	return len(fsys.unsettled.jobs)
}

// A check that cannot reach the server is queued, and the reconnect hook
// runs the queue; after maxSettleTries failures it is given up on.
func TestSettleIsRetriedAfterReconnect(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := l.Addr().String()
	l.Close()
	cfg := stallConfig(t, dead)
	cfg.ConnectTimeout = Duration(time.Second)
	log := NewLogger(LevelPanic)
	conn := NewConn(cfg, log)
	defer conn.Close()
	down := NewFS(cfg, conn, log, "/")
	defer down.Close()
	down.settleAside("/src", "/keep", "/.sshnfs-0123456789ab")
	if n := queued(down); n != 1 {
		t.Fatalf("%d checks queued after an unreachable server, want 1", n)
	}
	for i := 1; i < maxSettleTries; i++ {
		time.Sleep(dialBackoff) // let the dial be tried again
		down.settlePending()
	}
	if n := queued(down); n != 0 {
		t.Fatalf("%d checks still queued after %d failures", n, maxSettleTries)
	}

	// A real reconnect runs the queue through the hook.
	fsys, _ := sftpFixture(t)
	fsys.unsettled.mu.Lock()
	fsys.unsettled.jobs["/.sshnfs-0123456789ab"] = &settleJob{from: "/src", to: "/keep"}
	fsys.unsettled.mu.Unlock()
	_, gen, err := fsys.conn.Client()
	if err != nil {
		t.Fatal(err)
	}
	fsys.conn.Invalidate(gen)
	if _, _, err := fsys.conn.Client(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for queued(fsys) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the reconnect did not run the queued check")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A check that times out is not queued again: its timeout dropped the
// session, and rerunning it on every reconnect would keep dropping it.
func TestTimedOutSettleIsNotRetried(t *testing.T) {
	fsys, _ := sftpFixture(t)
	start := time.Now()
	fsys.settleAside("/src", "/stuck/x", "/stuck/.sshnfs-0123456789ab")
	if time.Since(start) > 3*time.Second {
		t.Fatalf("the check took %s", time.Since(start))
	}
	if n := queued(fsys); n != 0 {
		t.Fatalf("a timed-out check was queued again (%d queued)", n)
	}
}

// Shutdown gives up on what is still queued, and a check still running
// then does not queue itself again.
func TestCloseDrainsSettleQueue(t *testing.T) {
	fsys, _ := sftpFixture(t)
	fsys.unsettled.mu.Lock()
	fsys.unsettled.jobs["/.sshnfs-0123456789ab"] = &settleJob{from: "/src", to: "/keep"}
	fsys.unsettled.mu.Unlock()
	fsys.Close()
	if n := queued(fsys); n != 0 {
		t.Fatalf("%d checks still queued after Close", n)
	}
	fsys.settleAside("/src", "/keep", "/.sshnfs-ba9876543210")
	if n := queued(fsys); n != 0 {
		t.Fatalf("%d checks queued after Close", n)
	}
}

// The replaced destination is deleted after the rename, a directory too.
func TestRemoveReplacedDirectory(t *testing.T) {
	fsys, c := sftpFixture(t)
	if err := c.Mkdir("/src"); err != nil {
		t.Fatal(err)
	}
	if err := c.Mkdir("/dst"); err != nil {
		t.Fatal(err)
	}
	if err := replace(t, fsys, c, "/src", "/dst"); err != nil {
		t.Fatalf("directory onto an empty directory: %v", err)
	}
	if fi, err := c.Lstat("/dst"); err != nil || !fi.IsDir() || exists(t, c, "/src") {
		t.Fatalf("after rename: /dst %v %v, /src there=%v", fi, err, exists(t, c, "/src"))
	}
	noLeftovers(t, c, "/")
}

// A status that relays a lost connection counts as one; others do not.
func TestIsConnErrorStatusCodes(t *testing.T) {
	for code, want := range map[uint32]bool{2: false, 4: false, 6: true, 7: true} {
		if got := isConnError(&sftp.StatusError{Code: code}); got != want {
			t.Errorf("status %d: isConnError = %v, want %v", code, got, want)
		}
	}
}
