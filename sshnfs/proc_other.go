//go:build !unix

package main

import "os/exec"

// detachProcessGroup is a no-op where there are no Unix process groups.
func detachProcessGroup(*exec.Cmd) {}

func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}
