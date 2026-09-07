package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The local sshd doubles as its own jump host: connecting to 127.0.0.1:2222
// through 127.0.0.1:2222 is a real two-connection chain, with the second
// handshake running over a channel on the first.
func jumpSpec() string {
	return fmt.Sprintf("%s@127.0.0.1:2222", os.Getenv("SSHNFS_TEST_USER"))
}

func resolveProxyFor(t *testing.T, cfg *Config) {
	t.Helper()
	cfg.UseSSHConfig = false
	if err := cfg.applySSHConfig(); err != nil {
		t.Fatalf("resolve proxy: %v", err)
	}
}

func TestParseJumpSpec(t *testing.T) {
	cases := []struct {
		in    string
		hops  int
		first *endpoint
		bad   bool
	}{
		{in: "", hops: 0},
		{in: "none", hops: 0},
		{in: "host", hops: 1, first: &endpoint{Host: "host"}},
		{in: "user@host", hops: 1, first: &endpoint{User: "user", Host: "host"}},
		{in: "user@host:2222", hops: 1, first: &endpoint{User: "user", Host: "host", Port: 2222}},
		{in: "a, b@c:22 ,d", hops: 3, first: &endpoint{Host: "a"}},
		{in: "[2001:db8::1]:2222", hops: 1, first: &endpoint{Host: "2001:db8::1", Port: 2222}},
		{in: "u@[2001:db8::1]", hops: 1, first: &endpoint{User: "u", Host: "2001:db8::1"}},
		{in: "host:notaport", bad: true},
		{in: "user@", bad: true},
		{in: "[2001:db8::1:2222", bad: true},
	}
	for _, c := range cases {
		hops, err := parseJumpSpec(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("parseJumpSpec(%q) should have failed", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseJumpSpec(%q): %v", c.in, err)
			continue
		}
		if len(hops) != c.hops {
			t.Errorf("parseJumpSpec(%q) = %d hops, want %d", c.in, len(hops), c.hops)
			continue
		}
		if c.first != nil {
			got := hops[0]
			if got.User != c.first.User || got.Host != c.first.Host || got.Port != c.first.Port {
				t.Errorf("parseJumpSpec(%q)[0] = %+v, want %+v", c.in, got, c.first)
			}
		}
	}
}

func TestProxyJumpSingleHop(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	cfg.ProxyJump = jumpSpec()
	resolveProxyFor(t, cfg)

	if len(cfg.jumpHops) != 1 {
		t.Fatalf("got %d hops", len(cfg.jumpHops))
	}
	hop := cfg.jumpHops[0]
	if hop.Host != "127.0.0.1" || hop.Port != 2222 {
		t.Fatalf("hop = %s:%d", hop.Host, hop.Port)
	}
	if len(hop.IdentityFiles) == 0 {
		t.Fatal("hop did not inherit the target's identity files")
	}

	fsys := newTestFS(t, cfg)
	if err := os.WriteFile(filepath.Join(export, "through-jump.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := fsys.Stat("through-jump.txt")
	if err != nil {
		t.Fatalf("stat through a jump host: %v", err)
	}
	if fi.Size() != 2 {
		t.Fatalf("size = %d", fi.Size())
	}
}

func TestProxyJumpChain(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	cfg.ProxyJump = jumpSpec() + "," + jumpSpec()
	resolveProxyFor(t, cfg)
	if len(cfg.jumpHops) != 2 {
		t.Fatalf("got %d hops, want 2", len(cfg.jumpHops))
	}

	fsys := newTestFS(t, cfg)
	if _, err := fsys.ReadDir("."); err != nil {
		t.Fatalf("readdir over a two-hop chain: %v", err)
	}
}

func TestProxyJumpTooManyHops(t *testing.T) {
	cfg := testConfig(t, t.TempDir())
	cfg.UseSSHConfig = false
	cfg.ProxyJump = strings.TrimSuffix(strings.Repeat("h,", 9), ",")
	if err := cfg.applySSHConfig(); err == nil {
		t.Fatal("an absurdly long chain should be rejected")
	}
}

func TestProxyJumpFailureIsReported(t *testing.T) {
	cfg := testConfig(t, t.TempDir())
	cfg.ProxyJump = "127.0.0.1:1" // nothing listening
	cfg.ConnectTimeout = Duration(2 * time.Second)
	resolveProxyFor(t, cfg)

	conn := NewConn(cfg, NewLogger(LevelPanic))
	defer conn.Close()
	_, _, err := conn.Client()
	if err == nil {
		t.Fatal("expected the dial to fail")
	}
	if !strings.Contains(err.Error(), "jump host") {
		t.Fatalf("error should name the jump host, got: %v", err)
	}
}

func TestProxyCommand(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	// nc replaces the TCP connection; %h and %p are the target's.
	cfg.ProxyCommand = "nc %h %p"
	resolveProxyFor(t, cfg)

	fsys := newTestFS(t, cfg)
	if err := os.WriteFile(filepath.Join(export, "through-cmd.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Stat("through-cmd.txt"); err != nil {
		t.Fatalf("stat through a proxy command: %v", err)
	}
}

func TestProxyCommandFailureIsReported(t *testing.T) {
	cfg := testConfig(t, t.TempDir())
	cfg.ProxyCommand = "echo 'no route' >&2; exit 1"
	cfg.ConnectTimeout = Duration(2 * time.Second)
	resolveProxyFor(t, cfg)

	conn := NewConn(cfg, NewLogger(LevelPanic))
	defer conn.Close()
	if _, _, err := conn.Client(); err == nil {
		t.Fatal("a proxy command that exits should fail the dial")
	}
}

func TestProxyCommandHangIsBounded(t *testing.T) {
	cfg := testConfig(t, t.TempDir())
	// Accepts the connection and then says nothing at all.
	cfg.ProxyCommand = "sleep 60"
	cfg.ConnectTimeout = Duration(500 * time.Millisecond)
	resolveProxyFor(t, cfg)

	conn := NewConn(cfg, NewLogger(LevelPanic))
	defer conn.Close()
	start := time.Now()
	if _, _, err := conn.Client(); err == nil {
		t.Fatal("expected a timeout")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("dial took %v; the handshake was not bounded", elapsed)
	}
}

func TestProxyJumpFromSSHConfig(t *testing.T) {
	key := os.Getenv("SSHNFS_TEST_KEY")
	user := os.Getenv("SSHNFS_TEST_USER")
	if key == "" || user == "" {
		t.Skip("SSHNFS_TEST_KEY / SSHNFS_TEST_USER not set")
	}
	export := t.TempDir()
	sshCfg := writeSSHConfig(t, fmt.Sprintf(`
Host bastion
    HostName 127.0.0.1
    Port 2222
    User %[1]s
    IdentityFile %[2]s
    StrictHostKeyChecking no

Host inner
    HostName 127.0.0.1
    Port 2222
    User %[1]s
    IdentityFile %[2]s
    StrictHostKeyChecking no
    ProxyJump bastion
`, user, key))

	cfg := loadOne(t, "-F", sshCfg, "-agent=false", "inner:"+export)
	if len(cfg.jumpHops) != 1 {
		t.Fatalf("ProxyJump from ssh config gave %d hops", len(cfg.jumpHops))
	}
	hop := cfg.jumpHops[0]
	if hop.Host != "127.0.0.1" || hop.Port != 2222 || hop.User != user {
		t.Fatalf("hop = %s@%s:%d, want the bastion block resolved", hop.User, hop.Host, hop.Port)
	}
	if len(hop.IdentityFiles) != 1 || hop.IdentityFiles[0] != key {
		t.Fatalf("hop identity files = %v", hop.IdentityFiles)
	}

	fsys := newTestFS(t, cfg)
	if _, err := fsys.ReadDir("."); err != nil {
		t.Fatalf("readdir through an ssh config jump host: %v", err)
	}
}

func TestProxyCommandFromSSHConfig(t *testing.T) {
	sshCfg := writeSSHConfig(t, `
Host viacmd
    HostName 10.0.0.9
    Port 2200
    User someone
    ProxyCommand nc %h %p
`)
	cfg := loadOne(t, "-F", sshCfg, "viacmd")
	if cfg.ProxyCommand != "nc 10.0.0.9 2200" {
		t.Fatalf("ProxyCommand = %q, want the tokens expanded", cfg.ProxyCommand)
	}
}

func TestProxyJumpBeatsProxyCommand(t *testing.T) {
	sshCfg := writeSSHConfig(t, `
Host both
    HostName 10.0.0.9
    User someone
    ProxyJump bastion.example
    ProxyCommand nc %h %p
`)
	cfg := loadOne(t, "-F", sshCfg, "both")
	if len(cfg.jumpHops) != 1 {
		t.Fatalf("expected the jump host to be used, got %d hops", len(cfg.jumpHops))
	}
	if cfg.ProxyCommand != "" {
		t.Fatalf("ProxyCommand = %q, want it dropped in favour of ProxyJump", cfg.ProxyCommand)
	}
}

func TestProxyNoneDisablesInheritedValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{
	  "host":"h","user":"u","use_ssh_config":false,
	  "proxy_jump":"bastion.example",
	  "mounts":[
	    {"name":"viajump","remote_path":"/a"},
	    {"name":"direct","remote_path":"/b","proxy_jump":"none"}
	  ]
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	mounts, err := LoadConfig([]string{"-config", path}, os.Stderr)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(mounts[0].jumpHops) != 1 {
		t.Errorf("%s should have inherited the jump host", mounts[0].Label())
	}
	if len(mounts[1].jumpHops) != 0 || mounts[1].ProxyJump != "" {
		t.Errorf("%s should have disabled it with \"none\", got %q", mounts[1].Label(), mounts[1].ProxyJump)
	}
}

// A dropped session must rebuild the whole chain, not just the last hop.
func TestReconnectThroughJumpHost(t *testing.T) {
	export := t.TempDir()
	cfg := testConfig(t, export)
	cfg.ProxyJump = jumpSpec()
	cfg.Reconnect = true
	cfg.ReconnectDelay = Duration(150 * time.Millisecond)
	cfg.ReconnectMaxDelay = Duration(300 * time.Millisecond)
	resolveProxyFor(t, cfg)

	conn := NewConn(cfg, NewLogger(LevelError))
	defer conn.Close()
	if _, _, err := conn.Client(); err != nil {
		t.Fatalf("initial connect through jump host: %v", err)
	}
	conn.mu.Lock()
	first, transport, jumps := conn.gen, conn.ssh, len(conn.extra)
	conn.mu.Unlock()
	if jumps != 1 {
		t.Fatalf("session holds %d jump clients, want 1", jumps)
	}

	transport.Close()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, gen, ok := conn.current(); ok && gen > first {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no reconnection through the jump host")
		}
		time.Sleep(20 * time.Millisecond)
	}
	client, _, err := conn.Client()
	if err != nil {
		t.Fatalf("client after reconnect: %v", err)
	}
	if _, err := client.Stat(export); err != nil {
		t.Fatalf("stat after reconnect: %v", err)
	}
	conn.mu.Lock()
	jumps = len(conn.extra)
	conn.mu.Unlock()
	if jumps != 1 {
		t.Fatalf("after reconnect the session holds %d jump clients, want 1", jumps)
	}
}

// A jump host may have its own ProxyJump in ssh config; those hops belong in
// front of it, so "inner via bastion via edge" is one flat chain.
func TestNestedProxyJumpFromSSHConfig(t *testing.T) {
	key := os.Getenv("SSHNFS_TEST_KEY")
	user := os.Getenv("SSHNFS_TEST_USER")
	if key == "" || user == "" {
		t.Skip("SSHNFS_TEST_KEY / SSHNFS_TEST_USER not set")
	}
	export := t.TempDir()
	host := func(name, extra string) string {
		return fmt.Sprintf(`
Host %s
    HostName 127.0.0.1
    Port 2222
    User %s
    IdentityFile %s
    StrictHostKeyChecking no
%s
`, name, user, key, extra)
	}
	sshCfg := writeSSHConfig(t,
		host("edge", "")+
			host("bastion", "    ProxyJump edge")+
			host("inner", "    ProxyJump bastion"))

	cfg := loadOne(t, "-F", sshCfg, "-agent=false", "inner:"+export)
	if len(cfg.jumpHops) != 2 {
		t.Fatalf("got %d hops, want edge and bastion", len(cfg.jumpHops))
	}
	// Outermost first: edge is dialed directly, bastion through edge.
	for i, want := range []string{"edge", "bastion"} {
		if cfg.jumpHops[i].Host != "127.0.0.1" {
			t.Errorf("hop %d (%s) = %s, want the alias resolved", i, want, cfg.jumpHops[i].Host)
		}
	}

	fsys := newTestFS(t, cfg)
	if err := os.WriteFile(filepath.Join(export, "nested.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Stat("nested.txt"); err != nil {
		t.Fatalf("stat over a nested chain: %v", err)
	}
}

func TestNestedProxyJumpCycleIsRejected(t *testing.T) {
	sshCfg := writeSSHConfig(t, `
Host a
    HostName 10.0.0.1
    ProxyJump b

Host b
    HostName 10.0.0.2
    ProxyJump a

Host target
    HostName 10.0.0.3
    ProxyJump a
`)
	_, err := LoadConfig([]string{"-F", sshCfg, "-user", "u", "target"}, os.Stderr)
	if err == nil {
		t.Fatal("a ProxyJump cycle should be rejected, not followed")
	}
	if !strings.Contains(err.Error(), "reached through itself") {
		t.Fatalf("error = %v, want it to explain the cycle", err)
	}
}

func TestNestedProxyJumpDepthIsCapped(t *testing.T) {
	var b strings.Builder
	// h0 -> h1 -> ... -> h11, deeper than the recursion allows.
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "\nHost h%d\n    HostName 10.0.0.%d\n    ProxyJump h%d\n", i, i+1, i+1)
	}
	fmt.Fprintf(&b, "\nHost h12\n    HostName 10.0.0.99\n")
	fmt.Fprintf(&b, "\nHost target\n    HostName 10.0.1.1\n    ProxyJump h0\n")

	if _, err := LoadConfig([]string{"-F", writeSSHConfig(t, b.String()), "-user", "u", "target"}, os.Stderr); err == nil {
		t.Fatal("an over-deep ProxyJump chain should be rejected")
	}
}

// A jump host whose ssh config gives it a ProxyCommand is reached with that
// command, as long as nothing precedes it in the chain.
func TestJumpHostWithItsOwnProxyCommand(t *testing.T) {
	key := os.Getenv("SSHNFS_TEST_KEY")
	user := os.Getenv("SSHNFS_TEST_USER")
	if key == "" || user == "" {
		t.Skip("SSHNFS_TEST_KEY / SSHNFS_TEST_USER not set")
	}
	export := t.TempDir()
	sshCfg := writeSSHConfig(t, fmt.Sprintf(`
Host bastion
    HostName 127.0.0.1
    Port 2222
    User %[1]s
    IdentityFile %[2]s
    StrictHostKeyChecking no
    ProxyCommand nc %%h %%p

Host inner
    HostName 127.0.0.1
    Port 2222
    User %[1]s
    IdentityFile %[2]s
    StrictHostKeyChecking no
    ProxyJump bastion
`, user, key))

	cfg := loadOne(t, "-F", sshCfg, "-agent=false", "inner:"+export)
	if len(cfg.jumpHops) != 1 {
		t.Fatalf("got %d hops", len(cfg.jumpHops))
	}
	if cfg.jumpHops[0].ProxyCommand != "nc 127.0.0.1 2222" {
		t.Fatalf("hop proxy command = %q, want the tokens expanded", cfg.jumpHops[0].ProxyCommand)
	}

	fsys := newTestFS(t, cfg)
	if _, err := fsys.ReadDir("."); err != nil {
		t.Fatalf("readdir through a proxy-command bastion: %v", err)
	}
}
