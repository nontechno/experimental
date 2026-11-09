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
// With resvport set, the client is made to connect from a reserved port, which
// is what require_privileged_port checks for; a noresvport in base would make
// every connection be refused, so it is dropped.
func mountOptions(base string, port int, resvport bool) string {
	opts := []string{}
	for _, o := range strings.Split(base, ",") {
		if o = strings.TrimSpace(o); o != "" {
			if resvport && o == "noresvport" {
				continue
			}
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
	if resvport && !has("resvport") {
		opts = append(opts, "resvport")
	}
	if !has("port") {
		opts = append(opts, fmt.Sprintf("port=%d", port))
	}
	if !has("mountport") {
		opts = append(opts, fmt.Sprintf("mountport=%d", port))
	}
	return strings.Join(opts, ",")
}

// nfsSource is the host:/ argument of mount. An IPv6 address needs brackets,
// or "::1:/" cannot be split into host and path.
func nfsSource(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:/"
	}
	return host + ":/"
}

// MountCommand returns the command line that mounts the export, so it can be
// run or simply shown to the user.
func MountCommand(cfg *Config, host string, port int, mountPoint string) []string {
	argv := []string{
		"mount", "-t", "nfs",
		"-o", mountOptions(cfg.MountOptions, port, cfg.RequirePrivilegedPort),
		nfsSource(host),
		mountPoint,
	}
	if cfg.MountSudo {
		argv = append([]string{"sudo"}, argv...)
	}
	return argv
}

// waitDelay is how long a mount or umount may hold on to its output after
// its context expired. Killing sudo does not kill the mount_nfs or umount it
// started, and that child keeps our output pipe open, so without a delay
// CombinedOutput would wait for it indefinitely, timeout or not.
const waitDelay = 5 * time.Second

// runBounded runs argv with a timeout that holds even when the command's
// children outlive it.
func runBounded(ctx context.Context, timeout time.Duration, argv []string) ([]byte, error) {
	if timeout <= 0 {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.WaitDelay = waitDelay
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("no result after %s (%v)", timeout, err)
	}
	return out, err
}

func Mount(ctx context.Context, cfg *Config, host string, port int) error {
	argv := MountCommand(cfg, host, port, cfg.MountPoint)
	out, err := runBounded(ctx, cfg.MountTimeout.D(), argv)
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
		out, err := runBounded(context.Background(), cfg.UnmountTimeout.D(), argv)
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
