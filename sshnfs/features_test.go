package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nfsc "github.com/willscott/go-nfs-client/nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
)

// ---------- ~/.ssh/config ----------

func writeSSHConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh_config")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadOne(t *testing.T, args ...string) *Config {
	t.Helper()
	got, err := LoadConfig(args, os.Stderr)
	if err != nil {
		t.Fatalf("load %v: %v", args, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one mount, got %d", len(got))
	}
	return got[0]
}

func TestSSHConfigResolvesAlias(t *testing.T) {
	key := os.Getenv("SSHNFS_TEST_KEY")
	if key == "" {
		t.Skip("SSHNFS_TEST_KEY not set")
	}
	sshCfg := writeSSHConfig(t, fmt.Sprintf(`
Host other
    HostName 10.0.0.1

Host build*
    HostName 127.0.0.1
    Port 2222
    User %s
    IdentityFile %s
    IdentityFile ~/nonexistent-key
    StrictHostKeyChecking no
    ConnectTimeout 7
    ServerAliveInterval 11
`, os.Getenv("SSHNFS_TEST_USER"), key))

	cfg := loadOne(t, "-F", sshCfg, "buildhost:/tmp")

	if cfg.Host != "127.0.0.1" {
		t.Errorf("Host = %q, want the HostName from ssh config", cfg.Host)
	}
	if cfg.alias != "buildhost" {
		t.Errorf("alias = %q, want buildhost", cfg.alias)
	}
	if cfg.Port != 2222 {
		t.Errorf("Port = %d, want 2222", cfg.Port)
	}
	if cfg.User != os.Getenv("SSHNFS_TEST_USER") {
		t.Errorf("User = %q", cfg.User)
	}
	if len(cfg.IdentityFiles) != 1 || cfg.IdentityFiles[0] != key {
		t.Errorf("IdentityFiles = %v; the missing key should have been dropped", cfg.IdentityFiles)
	}
	if !cfg.InsecureHostKey {
		t.Error("StrictHostKeyChecking no was not honoured")
	}
	if cfg.ConnectTimeout.D() != 7*time.Second {
		t.Errorf("ConnectTimeout = %v, want 7s", cfg.ConnectTimeout)
	}
	if cfg.KeepAlive.D() != 11*time.Second {
		t.Errorf("KeepAlive = %v, want 11s", cfg.KeepAlive)
	}
	if cfg.RemotePath != "/tmp" {
		t.Errorf("RemotePath = %q", cfg.RemotePath)
	}
}

func TestSSHConfigDoesNotOverrideExplicitSettings(t *testing.T) {
	sshCfg := writeSSHConfig(t, `
Host alias
    HostName 10.0.0.1
    Port 2222
    User fromconfig
    ConnectTimeout 7
`)
	cfg := loadOne(t, "-F", sshCfg, "-port", "2200", "-connect-timeout", "3s", "chosen@alias")
	if cfg.Port != 2200 {
		t.Errorf("Port = %d, want the flag to win", cfg.Port)
	}
	if cfg.User != "chosen" {
		t.Errorf("User = %q, want the command line to win", cfg.User)
	}
	if cfg.ConnectTimeout.D() != 3*time.Second {
		t.Errorf("ConnectTimeout = %v, want the flag to win", cfg.ConnectTimeout)
	}
	if cfg.Host != "10.0.0.1" {
		t.Errorf("Host = %q, want the alias resolved", cfg.Host)
	}

	// And the whole mechanism can be switched off.
	off := loadOne(t, "-F", sshCfg, "-use-ssh-config=false", "-user", "u", "alias")
	if off.Host != "alias" || off.Port != 22 {
		t.Errorf("ssh config was consulted despite -use-ssh-config=false: %s:%d", off.Host, off.Port)
	}
}

func TestSSHConfigMissingFileIsFatalOnlyWhenNamed(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-config")
	if _, err := LoadConfig([]string{"-F", missing, "-user", "u", "somehost"}, os.Stderr); err == nil {
		t.Fatal("an explicitly named ssh config that does not exist should be an error")
	}
	// The default location is allowed to be absent.
	cfg := loadOne(t, "-F", "", "-user", "u", "somehost")
	if cfg.Host != "somehost" {
		t.Fatalf("Host = %q", cfg.Host)
	}
}

func TestSSHConfigConnects(t *testing.T) {
	key := os.Getenv("SSHNFS_TEST_KEY")
	user := os.Getenv("SSHNFS_TEST_USER")
	if key == "" || user == "" {
		t.Skip("SSHNFS_TEST_KEY / SSHNFS_TEST_USER not set")
	}
	export := t.TempDir()
	sshCfg := writeSSHConfig(t, fmt.Sprintf(`
Host testbox
    HostName 127.0.0.1
    Port 2222
    User %s
    IdentityFile %s
    StrictHostKeyChecking no
`, user, key))

	cfg := loadOne(t, "-F", sshCfg, "-agent=false", "testbox:"+export)
	fsys := newTestFS(t, cfg)

	if err := os.WriteFile(filepath.Join(export, "via-alias.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Stat("via-alias.txt"); err != nil {
		t.Fatalf("stat through an ssh config alias: %v", err)
	}
}

// ---------- background reconnect ----------

func TestBackgroundReconnectAfterTransportDies(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	cfg.Reconnect = true
	cfg.ReconnectDelay = Duration(150 * time.Millisecond)
	cfg.ReconnectMaxDelay = Duration(300 * time.Millisecond)

	conn := NewConn(cfg, NewLogger(LevelError))
	defer conn.Close()
	if _, _, err := conn.Client(); err != nil {
		t.Fatalf("initial connect: %v", err)
	}
	conn.mu.Lock()
	first := conn.gen
	transport := conn.ssh
	conn.mu.Unlock()

	// Kill the transport the way a dropped link would: watch() notices and
	// the supervisor reconnects on its own, with no request to prompt it.
	transport.Close()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, gen, ok := conn.current(); ok && gen > first {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no background reconnection happened")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// And the new session actually works.
	client, _, err := conn.Client()
	if err != nil {
		t.Fatalf("client after reconnect: %v", err)
	}
	if _, err := client.Stat(export); err != nil {
		t.Fatalf("stat after reconnect: %v", err)
	}
}

func TestReconnectDisabledStaysDown(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	cfg.Reconnect = false

	conn := NewConn(cfg, NewLogger(LevelError))
	defer conn.Close()
	if _, _, err := conn.Client(); err != nil {
		t.Fatalf("initial connect: %v", err)
	}
	conn.mu.Lock()
	transport := conn.ssh
	conn.mu.Unlock()
	transport.Close()

	// Give the watcher time to notice, then confirm nothing redialed.
	time.Sleep(500 * time.Millisecond)
	if _, _, ok := conn.current(); ok {
		t.Fatal("a session came back with reconnect disabled")
	}
	// A request still reconnects lazily, as before.
	if _, _, err := conn.Client(); err != nil {
		t.Fatalf("lazy reconnect on demand: %v", err)
	}
}

func TestReconnectGivesUpAfterMaxAttempts(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Host, cfg.Port = "127.0.0.1", 1 // nothing listening
	cfg.User = "nobody"
	cfg.UseAgent = false
	cfg.Password = "x"
	cfg.InsecureHostKey = true
	cfg.ConnectTimeout = Duration(200 * time.Millisecond)
	cfg.Reconnect = true
	cfg.ReconnectDelay = Duration(20 * time.Millisecond)
	cfg.ReconnectMaxDelay = Duration(40 * time.Millisecond)
	cfg.ReconnectMaxAttempts = 2

	conn := NewConn(cfg, NewLogger(LevelPanic))
	defer conn.Close()

	conn.scheduleReconnect()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn.mu.Lock()
		running := conn.reconnecting
		conn.mu.Unlock()
		if !running {
			return // it gave up, as instructed
		}
		if time.Now().After(deadline) {
			t.Fatal("the reconnector never gave up")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ---------- multiple mounts ----------

func multiConfig(t *testing.T, exportA, exportB string) string {
	t.Helper()
	body := map[string]interface{}{
		"host":                     "127.0.0.1",
		"port":                     2222,
		"user":                     os.Getenv("SSHNFS_TEST_USER"),
		"identity_files":           []string{os.Getenv("SSHNFS_TEST_KEY")},
		"use_agent":                false,
		"use_ssh_config":           false,
		"insecure_ignore_host_key": true,
		"listen":                   "127.0.0.1:0",
		"attr_cache_ttl":           "0s",
		"dir_cache_ttl":            "0s",
		"mounts": []map[string]interface{}{
			{"name": "alpha", "remote_path": exportA},
			{"name": "beta", "remote_path": exportB, "read_only": true},
		},
	}
	raw, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "multi.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMultipleMountsInherit(t *testing.T) {
	if os.Getenv("SSHNFS_TEST_KEY") == "" {
		t.Skip("SSHNFS_TEST_KEY not set")
	}
	a, b := t.TempDir(), t.TempDir()
	mounts, err := LoadConfig([]string{"-config", multiConfig(t, a, b)}, os.Stderr)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(mounts) != 2 {
		t.Fatalf("got %d mounts, want 2", len(mounts))
	}
	for _, m := range mounts {
		if m.Host != "127.0.0.1" || m.Port != 2222 {
			t.Errorf("%s did not inherit the shared host: %s:%d", m.Label(), m.Host, m.Port)
		}
		if len(m.IdentityFiles) != 1 {
			t.Errorf("%s did not inherit identity_files: %v", m.Label(), m.IdentityFiles)
		}
	}
	if mounts[0].Label() != "alpha" || mounts[1].Label() != "beta" {
		t.Fatalf("labels = %q, %q", mounts[0].Label(), mounts[1].Label())
	}
	if mounts[0].RemotePath != a || mounts[1].RemotePath != b {
		t.Fatal("per-mount remote_path was not applied")
	}
	if mounts[0].ReadOnly {
		t.Error("alpha should not be read-only")
	}
	if !mounts[1].ReadOnly {
		t.Error("beta should be read-only")
	}
	// A flag applies to every mount as a shared default.
	flagged, err := LoadConfig([]string{"-config", multiConfig(t, a, b), "-read-only"}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range flagged {
		if !m.ReadOnly {
			t.Errorf("%s ignored the -read-only flag", m.Label())
		}
	}
}

func TestMultipleMountsServeIndependently(t *testing.T) {
	if os.Getenv("SSHNFS_TEST_KEY") == "" {
		t.Skip("SSHNFS_TEST_KEY not set")
	}
	a, b := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(a, "in-alpha.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "in-beta.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	mounts, err := LoadConfig([]string{"-config", multiConfig(t, a, b)}, os.Stderr)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	log := NewLogger(LevelError)
	errCh := make(chan error, len(mounts))
	targets := map[string]*nfsc.Target{}
	for _, cfg := range mounts {
		srv, err := newServer(cfg, log, true)
		if err != nil {
			t.Fatalf("%s: %v", cfg.Label(), err)
		}
		defer srv.close()
		srv.serve(errCh)

		var c *rpc.Client
		for attempt := 0; attempt < 5; attempt++ {
			c, err = rpc.DialTCP("tcp", srv.listener.Addr().String(), false)
			if err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("rpc dial %s: %v", cfg.Label(), err)
		}
		defer c.Close()
		var mounter nfsc.Mount
		mounter.Client = c
		target, err := mounter.Mount("/", rpc.AuthNull)
		if err != nil {
			t.Fatalf("mount %s: %v", cfg.Label(), err)
		}
		defer mounter.Unmount()
		targets[cfg.Label()] = target
	}

	if _, _, err := targets["alpha"].Lookup("/in-alpha.txt"); err != nil {
		t.Fatalf("alpha does not export its own directory: %v", err)
	}
	if _, _, err := targets["alpha"].Lookup("/in-beta.txt"); err == nil {
		t.Fatal("alpha exposed beta's contents")
	}
	if _, _, err := targets["beta"].Lookup("/in-beta.txt"); err != nil {
		t.Fatalf("beta does not export its own directory: %v", err)
	}
	// beta is read-only, alpha is not.
	if _, err := targets["beta"].Create("/nope.txt", 0o644); err == nil {
		t.Fatal("beta accepted a write despite read_only")
	}
	if _, err := targets["alpha"].Create("/yes.txt", 0o644); err != nil {
		t.Fatalf("alpha refused a write: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("a server stopped early: %v", err)
	default:
	}
}

func TestMultipleMountsRejectConflicts(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		path := filepath.Join(t.TempDir(), "cfg.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	base := `"host":"h","user":"u","use_ssh_config":false,`

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"duplicate mount point",
			`{` + base + `"mounts":[{"mount_point":"` + dir + `"},{"mount_point":"` + dir + `"}]}`,
			"claimed by both",
		},
		{
			"duplicate fixed listen port",
			`{` + base + `"listen":"127.0.0.1:20999","mounts":[{"remote_path":"/a"},{"remote_path":"/b"}]}`,
			"claimed by both",
		},
		{
			"nested mounts",
			`{` + base + `"mounts":[{"mounts":[{"host":"x"}]}]}`,
			"nested",
		},
		{
			"empty mounts",
			`{` + base + `"mounts":[]}`,
			"empty",
		},
		{
			"unknown key inside a mount",
			`{` + base + `"mounts":[{"nosuchfield":1}]}`,
			"unknown field",
		},
		{
			"missing host in one mount",
			`{"user":"u","use_ssh_config":false,"mounts":[{"host":"a"},{"remote_path":"/b"}]}`,
			"no ssh host",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadConfig([]string{"-config", write(c.body)}, os.Stderr)
			if err == nil {
				t.Fatalf("expected an error mentioning %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want it to mention %q", err, c.want)
			}
		})
	}

	// Distinct auto-assigned ports are fine, even though both say :0.
	ok := write(`{` + base + `"listen":"127.0.0.1:0","mounts":[{"remote_path":"/a"},{"remote_path":"/b"}]}`)
	if _, err := LoadConfig([]string{"-config", ok}, os.Stderr); err != nil {
		t.Fatalf("port 0 should never collide: %v", err)
	}
}

func TestMountsArrayRejectsPositionalHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{"host":"h","user":"u","use_ssh_config":false,"mounts":[{"remote_path":"/a"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig([]string{"-config", path, "other@host:/x"}, os.Stderr); err == nil {
		t.Fatal("combining a mounts array with a positional host should fail")
	}
}
