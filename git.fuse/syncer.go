package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const (
	tickInterval   = 250 * time.Millisecond
	minBackoff     = 2 * time.Second
	maxBackoff     = 5 * time.Minute
	maxListedFiles = 50

	conflictLogInterval = 10 * time.Minute
)

// Syncer is the only component that touches the repository while mounted.
// It runs in a single goroutine, so repository operations never overlap.
//
// Policy:
//   - Commit when changes have been quiet for commit.quiet_period and no
//     file is open for writing, or unconditionally once changes are
//     commit.max_delay old.
//   - Fetch every sync.fetch_interval and after each commit. Upstream
//     changes are applied to the work tree and unpushed local changes are
//     re-committed on top (path-level rebase). This rewrites work tree
//     files, so it only happens with no file open for writing.
//   - Push without force. A rejected push is retried after fetch.
//   - If both sides changed the same path, nothing is modified: pulling
//     is suspended until restart; local commits continue.
type Syncer struct {
	cfg  *Config
	repo *Repo
	gate *Gate
	log  *slog.Logger

	remotePending bool
	lastFetch     time.Time
	conflict      error
	lastConflict  time.Time // last time the suspension was logged

	commitFailures int
	nextCommit     time.Time
	remoteFailures int
	nextRemote     time.Time
}

func NewSyncer(cfg *Config, repo *Repo, gate *Gate, log *slog.Logger) *Syncer {
	return &Syncer{cfg: cfg, repo: repo, gate: gate, log: log, remotePending: true}
}

var errDeferred = errors.New("deferred: files are open for writing")

func (s *Syncer) Run(ctx context.Context) {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.step(ctx, time.Now())
		}
	}
}

func (s *Syncer) step(ctx context.Context, now time.Time) {
	if s.shouldCommit(now) && !now.Before(s.nextCommit) {
		n, err := s.commit()
		if err != nil {
			s.commitFailures++
			s.nextCommit = now.Add(backoff(s.commitFailures))
			s.log.Error("commit failed; changes remain in the work tree and will be retried",
				"err", err, "retry_in", backoff(s.commitFailures))
		} else {
			s.commitFailures = 0
			if n > 0 {
				s.remotePending = true
			}
		}
	}

	if fi := s.cfg.Sync.FetchInterval.Duration; fi > 0 && now.Sub(s.lastFetch) >= fi {
		s.remotePending = true
	}
	if s.remotePending && !now.Before(s.nextRemote) {
		switch err := s.syncRemote(ctx, false); {
		case err == nil:
			s.remotePending = false
			s.remoteFailures = 0
		case errors.Is(err, errDeferred):
			// Retry on the next tick; not a failure.
		default:
			s.remoteFailures++
			s.nextRemote = now.Add(backoff(s.remoteFailures))
			s.log.Error("remote sync failed", "err", err, "retry_in", backoff(s.remoteFailures))
		}
	}
}

func (s *Syncer) shouldCommit(now time.Time) bool {
	if !s.gate.Dirty() {
		return false
	}
	if now.Sub(s.gate.DirtySince()) >= s.cfg.Commit.MaxDelay.Duration {
		return true
	}
	return now.Sub(s.gate.LastChange()) >= s.cfg.Commit.QuietPeriod.Duration && s.gate.Writers() == 0
}

// commit records the work tree while FUSE mutations are blocked, so the
// snapshot never contains a half-applied operation.
func (s *Syncer) commit() (files int, err error) {
	err = s.gate.Exclusive(func() error {
		if !s.gate.takeDirtyLocked() {
			return nil
		}
		files, err = s.repo.Commit(s.cfg.Commit.Message)
		if err != nil {
			s.gate.markDirtyLocked() // make sure it is retried
		}
		return err
	})
	if files > 0 {
		s.log.Info("committed", "files", files)
	}
	return files, err
}

// syncRemote fetches, integrates upstream changes, and pushes. With final
// set (after unmount) the writer count is ignored: no handles can remain.
func (s *Syncer) syncRemote(ctx context.Context, final bool) error {
	if err := s.repo.Fetch(ctx); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	s.lastFetch = time.Now()

	rel, err := s.repo.Relation()
	if err != nil {
		return fmt.Errorf("compare with upstream: %w", err)
	}
	if s.conflict != nil && (rel == Behind || rel == Diverged) {
		if time.Since(s.lastConflict) >= conflictLogInterval {
			s.lastConflict = time.Now()
			s.log.Error("pulling suspended until restart; resolve manually", "reason", s.conflict, "state", rel)
		}
		return nil
	}

	if rel == Behind || rel == Diverged {
		if !final && (s.gate.Writers() > 0 || s.gate.Dirty()) {
			return errDeferred
		}
		err := s.gate.Exclusive(func() error {
			// Re-check under the lock: the count cannot change now.
			if !final && (s.gate.Writers() > 0 || s.gate.Dirty()) {
				return errDeferred
			}
			n, err := s.repo.Integrate()
			var ce *ConflictError
			if err != nil && !errors.As(err, &ce) {
				// The work tree may have been partly updated;
				// make sure the next commit records it.
				s.gate.markDirtyLocked()
			}
			if err == nil {
				s.log.Info("integrated upstream changes", "was", rel, "local_files_recommitted", n)
			}
			return err
		})
		var ce *ConflictError
		if errors.As(err, &ce) {
			s.conflict = ce
			s.lastConflict = time.Now()
			s.log.Error("pulling suspended until restart; local changes kept and still committed locally", "err", ce)
			return nil
		}
		if err != nil {
			if errors.Is(err, errDeferred) {
				return err
			}
			return fmt.Errorf("integrate upstream: %w", err)
		}
		if rel, err = s.repo.Relation(); err != nil {
			return fmt.Errorf("compare with upstream: %w", err)
		}
	}

	if rel == Ahead && s.cfg.Sync.Push {
		h, err := s.repo.Push(ctx)
		if err != nil {
			return fmt.Errorf("push: %w", err) // typically a race with another pusher; retried after fetch
		}
		s.log.Info("pushed", "commit", h.String()[:12])
	}
	return nil
}

// Final commits any remaining changes and makes one sync attempt. It is
// called after the filesystem has been unmounted.
func (s *Syncer) Final(ctx context.Context) {
	if _, err := s.commit(); err != nil {
		s.log.Error("final commit failed; changes remain in the work tree", "err", err)
		return
	}
	if err := s.syncRemote(ctx, true); err != nil {
		s.log.Error("final sync failed; unpushed commits remain in the state dir and are pushed on next start", "err", err)
	}
}

func backoff(failures int) time.Duration {
	d := minBackoff
	for i := 1; i < failures && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}
