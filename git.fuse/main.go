// Command gitmount mounts one branch of a git repository as a read/write
// FUSE filesystem. Changes are committed automatically and pushed to the
// remote; upstream changes are pulled in periodically.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

func main() {
	cfgPath := flag.String("config", "/etc/gitmount/gitmount.toml", "path to the TOML config file")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*cfgPath, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cfgPath string, log *slog.Logger) error {
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	// Fail fast on unreadable or insecure credentials.
	if _, err := authMethod(cfg); err != nil {
		return err
	}

	paths := NewPaths(cfg.State.Dir)
	lock, err := LockStateDir(paths)
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := checkMountpoint(cfg.Mount.Path); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	repo, err := PrepareRepo(ctx, cfg, paths, log)
	if err != nil {
		return err
	}
	defer repo.Close()

	gate := &Gate{}
	syncer := NewSyncer(cfg, repo, gate, log)
	// Pick up anything left uncommitted by a previous crash.
	gate.MarkDirty()

	root, err := NewRootNode(paths.WorkTree, gate)
	if err != nil {
		return err
	}
	server, err := fs.Mount(cfg.Mount.Path, root, MountOptions(cfg))
	if err != nil {
		return fmt.Errorf("mount %s: %w", cfg.Mount.Path, err)
	}
	log.Info("mounted", "path", cfg.Mount.Path, "branch", cfg.Repo.Branch)

	syncCtx, cancelSync := context.WithCancel(context.Background())
	syncDone := make(chan struct{})
	go func() {
		defer close(syncDone)
		syncer.Run(syncCtx)
	}()

	go func() {
		<-ctx.Done()
		stop() // a second signal now terminates immediately
		log.Info("shutting down")
		unmount(server, cfg.Mount.Path, cfg.Shutdown.UnmountTimeout.Duration, log)
	}()

	// Returns once the filesystem is unmounted, by us or externally
	// (fusermount3 -u).
	server.Wait()
	log.Info("unmounted")

	cancelSync()
	<-syncDone
	finalCtx, cancel := context.WithTimeout(context.Background(), 3*cfg.Sync.NetworkTimeout.Duration)
	defer cancel()
	syncer.Final(finalCtx)
	return nil
}

// unmount tries a clean unmount until timeout, then falls back to a lazy
// unmount: the mount disappears from the namespace immediately, processes
// with open files keep working until they close them, and server.Wait
// returns once the last one does. Nothing is discarded either way.
func unmount(server *fuse.Server, mountpoint string, timeout time.Duration, log *slog.Logger) {
	deadline := time.Now().Add(timeout)
	for {
		err := server.Unmount()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			log.Warn("mount still busy; detaching lazily", "err", err)
			break
		}
		log.Warn("unmount failed, retrying", "err", err)
		time.Sleep(time.Second)
	}
	if os.Geteuid() == 0 {
		if err := syscall.Unmount(mountpoint, syscall.MNT_DETACH); err == nil {
			return
		} else {
			log.Error("lazy unmount failed", "err", err)
		}
	}
	for _, bin := range []string{"fusermount3", "fusermount"} {
		out, err := exec.Command(bin, "-u", "-z", mountpoint).CombinedOutput()
		if err == nil {
			return
		}
		if !errors.Is(err, exec.ErrNotFound) {
			log.Error("lazy unmount failed", "cmd", bin, "err", err, "output", string(out))
		}
	}
}
