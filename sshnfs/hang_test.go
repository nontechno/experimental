package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Regressions for operations that used to block forever. These run against
// an in-process SSH server, so they need no external sshd.

// stallingSSHD is an SFTP server whose Stat of /stuck never answers, the way
// sftp-server behaves when the path lives on a hung or locked filesystem.
type stallingSSHD struct {
	addr    string
	release chan struct{}
	ln      net.Listener

	mu    sync.Mutex
	conns []net.Conn

	// muteGlobal stops answering global requests such as keepalives, the
	// way a link that silently drops packets looks from the client.
	muteGlobal atomic.Bool
}

type stallLister struct {
	base    sftp.FileLister
	release chan struct{}
}

func (s stallLister) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	if strings.HasPrefix(r.Filepath, "/stuck") {
		select {
		case <-s.release:
		case <-r.Context().Done():
		}
		return nil, os.ErrNotExist
	}
	return s.base.Filelist(r)
}

func startStallingSSHD(t *testing.T) *stallingSSHD {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil },
		// Offered but always refused, so clients do consult their keys and agent.
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, errors.New("no")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := &stallingSSHD{addr: ln.Addr().String(), release: make(chan struct{}), ln: ln}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			d.mu.Lock()
			d.conns = append(d.conns, nc)
			d.mu.Unlock()
			go d.serve(nc, cfg)
		}
	}()
	t.Cleanup(d.close)
	return d
}

func (d *stallingSSHD) serve(nc net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	go func() { // answers keepalives, as sshd does, unless muted
		for req := range reqs {
			if d.muteGlobal.Load() {
				continue
			}
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}()
	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range creqs {
				ok := req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp"
				req.Reply(ok, nil)
				if ok {
					h := sftp.InMemHandler()
					h.FileList = stallLister{base: h.FileList, release: d.release}
					go sftp.NewRequestServer(ch, h).Serve()
				}
			}
		}()
	}
}

func (d *stallingSSHD) close() {
	select {
	case <-d.release:
	default:
		close(d.release)
	}
	d.ln.Close()
	d.mu.Lock()
	for _, c := range d.conns {
		c.Close()
	}
	d.mu.Unlock()
}

func stallConfig(t *testing.T, addr string) *Config {
	t.Helper()
	host, port, _ := net.SplitHostPort(addr)
	cfg := DefaultConfig()
	cfg.Host = host
	cfg.Port, _ = strconv.Atoi(port)
	cfg.User = "tester"
	cfg.Password = "secret"
	cfg.UseAgent = false
	cfg.IdentityFiles = []string{} // keep the user's own keys out of it
	cfg.InsecureHostKey = true
	cfg.ConnectTimeout = Duration(3 * time.Second)
	cfg.OpTimeout = Duration(300 * time.Millisecond)
	cfg.ReconnectDelay = Duration(50 * time.Millisecond)
	cfg.ReconnectMaxDelay = Duration(200 * time.Millisecond)
	return cfg
}

func TestStalledRequestTimesOutAndRecovers(t *testing.T) {
	d := startStallingSSHD(t)
	cfg := stallConfig(t, d.addr)
	log := NewLogger(LevelPanic)
	conn := NewConn(cfg, log)
	defer conn.Close()
	fsys := NewFS(cfg, conn, log, "/")
	defer fsys.Close()

	_, gen1, err := conn.Client()
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	start := time.Now()
	_, err = fsys.Stat("/stuck")
	elapsed := time.Since(start)
	var te *opTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("stat of a stalled path: got %v, want an op timeout", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("stat took %s; the op timeout is %s", elapsed, cfg.OpTimeout)
	}
	if isConnError(err) {
		t.Errorf("a timeout must not be retried as a connection error")
	}

	// The stuck session was dropped and a fresh one serves other paths.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := fsys.Stat("/")
		_, gen2, ok := conn.current()
		if err == nil && ok && gen2 > gen1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no recovery after the timeout: stat / = %v, gen %d (was %d)", err, gen2, gen1)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestShutdownDoesNotWaitOnStalledServer(t *testing.T) {
	d := startStallingSSHD(t)
	cfg := stallConfig(t, d.addr)
	cfg.OpTimeout = 0 // the old unbounded behaviour; Close alone must cope
	cfg.Reconnect = false
	log := NewLogger(LevelPanic)
	conn := NewConn(cfg, log)
	fsys := NewFS(cfg, conn, log, "/")

	if _, err := fsys.Stat("/"); err != nil {
		t.Fatalf("stat /: %v", err)
	}
	stuck := make(chan error, 1)
	go func() {
		_, err := fsys.Stat("/stuck")
		stuck <- err
	}()
	time.Sleep(200 * time.Millisecond) // let it reach the server

	closed := make(chan struct{})
	go func() {
		conn.Close() // same order as server.close
		fsys.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("closing the connection blocked on a stalled request")
	}
	select {
	case err := <-stuck:
		if err == nil {
			t.Fatal("stalled stat returned success after close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stalled request was not released by closing the connection")
	}
}

func TestEncryptedKeyIsNotPromptedForWhenNotInteractive(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_test")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = loadPrivateKey(path, "", false)
	if err == nil || !strings.Contains(err.Error(), "no passphrase is known") {
		t.Fatalf("got %v, want a refusal to prompt", err)
	}
	if _, err := loadPrivateKey(path, "pw", false); err != nil {
		t.Fatalf("with the passphrase: %v", err)
	}
}

func TestReconnectNeverPromptsForPassword(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UseAgent = false
	conn := NewConn(cfg, NewLogger(LevelPanic))
	conn.gen = 1 // pretend the first connection already happened
	e := &endpoint{User: "u", Host: "h", Port: 22, AskPassword: true, IdentityFiles: []string{}}

	// IdentityFiles is empty, so default keys may be found; what matters is
	// that the password is neither prompted for nor reported as usable.
	_, _, err := conn.authMethods(e)
	if err != nil && !strings.Contains(err.Error(), "first connection") {
		t.Fatalf("unexpected error: %v", err)
	}

	secrets.setPassword("u@h:22", "remembered")
	defer secrets.setPassword("u@h:22", "")
	if _, _, err := conn.authMethods(e); err != nil {
		t.Fatalf("with a remembered password: %v", err)
	}
}

func TestZeroConnectTimeoutFallsBackToDefault(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	os.WriteFile(kh, nil, 0o600)
	cfg := loadOne(t, "-use-ssh-config=false", "-known-hosts", kh, "-connect-timeout", "0", "u@h")
	if cfg.ConnectTimeout != DefaultConfig().ConnectTimeout {
		t.Fatalf("ConnectTimeout = %s, want the default", cfg.ConnectTimeout)
	}

	sc := writeSSHConfig(t, "Host h\n    ConnectTimeout 0\n")
	cfg = loadOne(t, "-F", sc, "-known-hosts", kh, "u@h")
	if cfg.ConnectTimeout != DefaultConfig().ConnectTimeout {
		t.Fatalf("ssh config ConnectTimeout 0 gave %s, want the default", cfg.ConnectTimeout)
	}
}

func TestMountCommandTimeoutHoldsWhenChildKeepsPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh")
	}
	// sh starts a grandchild that inherits the output pipe and outlives the
	// kill, which is what sudo -> mount_nfs looks like.
	start := time.Now()
	_, err := runBounded(context.Background(), 200*time.Millisecond, []string{"/bin/sh", "-c", "sleep 60 & sleep 60"})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > waitDelay+5*time.Second {
		t.Fatalf("runBounded returned after %s", elapsed)
	}
}

func TestOverdueTracksSilenceNotDuration(t *testing.T) {
	start := time.Now()
	act := newActivity()
	quiet, hard := time.Second, time.Minute

	// Server answered recently: a long operation is fine.
	act.last.Store(start.Add(50 * time.Second).UnixNano())
	if why, wait := overdue(start.Add(50*time.Second+500*time.Millisecond), start, act, quiet, hard); why != "" || wait != 500*time.Millisecond {
		t.Fatalf("busy session: why=%q wait=%s", why, wait)
	}
	// Server silent for the whole quiet period.
	if why, _ := overdue(start.Add(52*time.Second), start, act, quiet, hard); !strings.Contains(why, "no reply") {
		t.Fatalf("silent session: why=%q", why)
	}
	// Busy, but past the hard cap.
	act.last.Store(start.Add(time.Minute).UnixNano())
	if why, _ := overdue(start.Add(time.Minute), start, act, quiet, hard); !strings.Contains(why, "still running") {
		t.Fatalf("hard cap: why=%q", why)
	}
	// Activity from before the operation started does not count.
	act.last.Store(start.Add(-time.Hour).UnixNano())
	if why, wait := overdue(start.Add(200*time.Millisecond), start, act, quiet, hard); why != "" || wait != 800*time.Millisecond {
		t.Fatalf("fresh op: why=%q wait=%s", why, wait)
	}
}

func TestUnansweredKeepaliveDropsSession(t *testing.T) {
	d := startStallingSSHD(t)
	cfg := stallConfig(t, d.addr)
	cfg.KeepAlive = Duration(100 * time.Millisecond)
	cfg.KeepAliveTimeout = Duration(300 * time.Millisecond)
	cfg.Reconnect = false
	conn := NewConn(cfg, NewLogger(LevelPanic))
	defer conn.Close()

	if _, _, err := conn.Client(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	d.muteGlobal.Store(true)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, _, ok := conn.current(); !ok {
			return // dropped, as it should be
		}
		if time.Now().After(deadline) {
			t.Fatal("session still considered alive although keepalives go unanswered")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestWaitingForDialIsBounded(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Host, cfg.User = "127.0.0.1", "u"
	cfg.OpTimeout = Duration(200 * time.Millisecond)
	conn := NewConn(cfg, NewLogger(LevelPanic))
	defer conn.Close()

	conn.dialing <- struct{}{} // a dial that never finishes
	start := time.Now()
	_, _, err := conn.Client()
	if err == nil || !strings.Contains(err.Error(), "still connecting") {
		t.Fatalf("got %v, want a still-connecting error", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waited %s for the dial lock", elapsed)
	}
}

func TestHungAgentDoesNotHangHandshake(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Read requests, never answer.
			go func() {
				buf := make([]byte, 4096)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)

	d := startStallingSSHD(t)
	cfg := stallConfig(t, d.addr)
	cfg.UseAgent = true
	cfg.ConnectTimeout = Duration(500 * time.Millisecond)
	conn := NewConn(cfg, NewLogger(LevelPanic))
	defer conn.Close()

	done := make(chan error, 1)
	go func() { _, _, err := conn.Client(); done <- err }()
	select {
	case <-done:
		// Success (password after the agent gave up) or an error: either
		// way it came back.
	case <-time.After(5 * time.Second):
		t.Fatal("handshake blocked on an unresponsive ssh-agent")
	}
}

func TestLogSinkNeverBlocks(t *testing.T) {
	out := &stuckWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(out.release)
	w := newAsyncWriter(out, 4)
	// Wait until the writer goroutine is stuck in a write. Otherwise it may
	// pick up the queue only after the burst below, report the drops (which
	// resets the counter) and leave nothing to check.
	w.Write([]byte("first\n"))
	<-out.entered
	start := time.Now()
	for i := 0; i < 100; i++ {
		w.Write([]byte("line\n"))
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("writes blocked for %s", elapsed)
	}
	if w.dropped.Load() == 0 {
		t.Error("expected dropped lines to be counted")
	}
	start = time.Now()
	w.Flush(200 * time.Millisecond)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Flush ignored its deadline: %s", elapsed)
	}
}

// stuckWriter blocks every Write until released, and says when one started.
type stuckWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (w *stuckWriter) Write(p []byte) (int, error) {
	select {
	case w.entered <- struct{}{}:
	default:
	}
	<-w.release
	return len(p), nil
}
