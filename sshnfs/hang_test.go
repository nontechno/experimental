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
	go ssh.DiscardRequests(reqs) // answers keepalives, as sshd does
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
	_, err := conn.authMethods(e)
	if err != nil && !strings.Contains(err.Error(), "first connection") {
		t.Fatalf("unexpected error: %v", err)
	}

	conn.passwords["u@h:22"] = "remembered"
	if _, err := conn.authMethods(e); err != nil {
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
