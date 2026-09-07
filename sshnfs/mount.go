package main

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// mountOptions makes sure the caller supplied options carry the port the
// server actually listened on. Both port and mountport are required because
// the server does not register with rpcbind.
func mountOptions(base string, port int) string {
	opts := []string{}
	for _, o := range strings.Split(base, ",") {
		if o = strings.TrimSpace(o); o != "" {
			opts = append(opts, o)
		}
	}
	has := func(name string) bool {
		for _, o := range opts {
			if o == name || strings.HasPrefix(o, name+"=") {
				return true
			}
		}
		return false
	}
	if !has("port") {
		opts = append(opts, fmt.Sprintf("port=%d", port))
	}
	if !has("mountport") {
		opts = append(opts, fmt.Sprintf("mountport=%d", port))
	}
	return strings.Join(opts, ",")
}

// MountCommand returns the command line that mounts the export, so it can be
// run or simply shown to the user.
func MountCommand(cfg *Config, host string, port int, mountPoint string) []string {
	argv := []string{
		"mount", "-t", "nfs",
		"-o", mountOptions(cfg.MountOptions, port),
		fmt.Sprintf("%s:/", host),
		mountPoint,
	}
	if cfg.MountSudo {
		argv = append([]string{"sudo"}, argv...)
	}
	return argv
}

func Mount(ctx context.Context, cfg *Config, host string, port int) error {
	argv := MountCommand(cfg, host, port, cfg.MountPoint)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		hint := ""
		if runtime.GOOS == "darwin" && !cfg.MountSudo {
			hint = " (mounting NFS on macOS normally needs root: try -mount-sudo)"
		}
		if msg != "" {
			return fmt.Errorf("%s: %v: %s%s", strings.Join(argv, " "), err, msg, hint)
		}
		return fmt.Errorf("%s: %v%s", strings.Join(argv, " "), err, hint)
	}
	return nil
}

func Unmount(cfg *Config) error {
	if cfg.MountPoint == "" {
		return nil
	}
	attempts := [][]string{{"umount", cfg.MountPoint}}
	if runtime.GOOS == "darwin" {
		attempts = append(attempts,
			[]string{"umount", "-f", cfg.MountPoint},
			[]string{"diskutil", "unmount", "force", cfg.MountPoint},
		)
	} else {
		attempts = append(attempts, []string{"umount", "-l", cfg.MountPoint})
	}

	var lastErr error
	for _, argv := range attempts {
		if cfg.MountSudo {
			argv = append([]string{"sudo"}, argv...)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
		cancel()
		if err == nil {
			return nil
		}
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "not currently mounted") || strings.Contains(msg, "not mounted") {
			return nil
		}
		lastErr = fmt.Errorf("%s: %v: %s", strings.Join(argv, " "), err, msg)
	}
	return lastErr
}
