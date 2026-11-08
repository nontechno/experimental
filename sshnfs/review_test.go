package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
