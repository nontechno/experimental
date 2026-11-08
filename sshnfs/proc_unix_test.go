//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Closing a detached ProxyCommand kills what the shell started, not just the
// shell, and the command does not share our process group (so a Ctrl-C at the
// terminal does not reach it).
func TestProxyCommandProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	cmd := "sleep 300 & echo $! > " + pidFile + "; wait"
	nc, err := dialProxyCommand(cmd, &endpoint{Host: "h", Port: 22}, NewLogger(LevelPanic), true)
	if err != nil {
		t.Fatal(err)
	}
	cc := nc.(*cmdConn)
	if pgid, err := syscall.Getpgid(cc.cmd.Process.Pid); err != nil || pgid == syscall.Getpgrp() {
		t.Fatalf("proxy command shares our process group (pgid %d, err %v)", pgid, err)
	}

	var pid int
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(pidFile)
		if pid, _ = strconv.Atoi(strings.TrimSpace(string(b))); pid > 0 {
			break
		}
	}
	if pid <= 0 {
		t.Fatal("the command never started its child")
	}
	nc.Close()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatal("the proxy command's child outlived Close")
}

func TestMountedOn(t *testing.T) {
	dir := t.TempDir()
	if on, err := mountedOn(dir, 5*time.Second); err != nil || on {
		t.Fatalf("mountedOn(%s) = %v, %v; want false", dir, on, err)
	}
	if on, err := mountedOn("/", 5*time.Second); err != nil || on {
		t.Fatalf("mountedOn(/) = %v, %v; want false", on, err)
	}
}

// A proxy command that writes a line longer than the scanner's buffer to
// stderr must not stall: its stderr keeps being drained.
func TestProxyCommandLongStderrLine(t *testing.T) {
	cmd := `head -c 300000 /dev/zero | tr "\0" x >&2; echo done`
	nc, err := dialProxyCommand(cmd, &endpoint{Host: "h", Port: 22}, NewLogger(LevelPanic), true)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	got := make(chan string, 1)
	go func() {
		b := make([]byte, 16)
		n, _ := nc.Read(b)
		got <- string(b[:n])
	}()
	select {
	case s := <-got:
		if !strings.HasPrefix(s, "done") {
			t.Fatalf("read %q, want done", s)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the proxy command stalled on its own stderr")
	}
}
