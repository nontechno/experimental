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
