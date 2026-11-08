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
