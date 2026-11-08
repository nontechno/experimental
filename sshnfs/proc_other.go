//go:build !unix

package main

import (
	"os/exec"
	"time"
)

// detachProcessGroup is a no-op where there are no Unix process groups.
func detachProcessGroup(*exec.Cmd) {}

func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}

// mountedOn cannot tell without Unix device numbers; it assumes not.
func mountedOn(string, time.Duration) (bool, error) { return false, nil }
