//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// detachProcessGroup puts a helper command in a process group of its own.
// Two reasons: a Ctrl-C at the terminal goes to the whole foreground group,
// and would kill a ProxyCommand (and the connection running over it) before
// the orderly shutdown has unmounted anything; and a shell command can start
// children of its own (a pipeline, nc), which killing the shell alone would
// leave behind on every reconnect.
//
// The price is that a process outside the foreground group cannot read the
// terminal, so a command that needs to prompt (ssh -W asking for a jump
// host's password) is left attached; see dialProxyCommand.
func detachProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcess kills a helper command, with everything it started when it has
// a process group of its own.
func killProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.Process.Kill()
}
