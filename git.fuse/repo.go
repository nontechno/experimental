package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// State directory layout (all owned by gitmount; nothing else may write here):
//
//	<state.dir>/lock          flock(2) held for the lifetime of the process
//	<state.dir>/git/          git metadata (kept outside the work tree)
//	<state.dir>/worktree/     checked-out files; this is what gets mounted
//	<state.dir>/initialized   written only after a complete initial checkout
type Paths struct {
	Root, Lock, GitDir, WorkTree, Marker string
}

func NewPaths(stateDir string) Paths {
	return Paths{
		Root:     stateDir,
		Lock:     filepath.Join(stateDir, "lock"),
		GitDir:   filepath.Join(stateDir, "git"),
		WorkTree: filepath.Join(stateDir, "worktree"),
		Marker:   filepath.Join(stateDir, "initialized"),
	}
}

// LockStateDir takes an exclusive, non-blocking flock so that two instances
// can never operate on the same repository. The returned file must stay open.
func LockStateDir(p Paths) (*os.File, error) {
	// 0700: only the service user can reach the backing files directly.
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p.Lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("state dir %s is in use by another gitmount instance", p.Root)
		}
		return nil, err
	}
	return f, nil
}

const remoteName = "origin"

// Relation of local HEAD to the remote-tracking branch.
type Relation int

const (
	Equal Relation = iota
	Ahead
	Behind
	Diverged
)

func (r Relation) String() string {
	return [...]string{"equal", "ahead", "behind", "diverged"}[r]
}

// ConflictError means local and upstream changes cannot be combined
// automatically. Nothing has been modified when it is returned.
type ConflictError struct {
	Reason string
	Paths  []string
}

func (e *ConflictError) Error() string {
	if len(e.Paths) == 0 {
		return "conflict: " + e.Reason
	}
	ps := e.Paths
	if len(ps) > 10 {
		ps = append(ps[:10:10], fmt.Sprintf("... (%d more)", len(e.Paths)-10))
	}
	return fmt.Sprintf("conflict: %s: %s", e.Reason, strings.Join(ps, ", "))
}

// Repo is a clone managed entirely by go-git; no git binary is used.
type Repo struct {
	cfg   *Config
	paths Paths
	log   *slog.Logger
	r     *git.Repository
	wt    *git.Worktree
	root  *os.Root // the work tree; all file writes by gitmount go through it
}

func (r *Repo) branchRef() plumbing.ReferenceName {
	return plumbing.NewBranchReferenceName(r.cfg.Repo.Branch)
}

func (r *Repo) remoteRef() plumbing.ReferenceName {
	return plumbing.NewRemoteReferenceName(remoteName, r.cfg.Repo.Branch)
}

func (r *Repo) fetchSpec() config.RefSpec {
	return config.RefSpec("+" + r.branchRef().String() + ":" + r.remoteRef().String())
}

func (r *Repo) Close() error { return r.root.Close() }

// PrepareRepo creates the local clone on first run, or validates and
// recovers an existing one. Call it while holding the state lock and
// before mounting.
func PrepareRepo(ctx context.Context, cfg *Config, p Paths, log *slog.Logger) (*Repo, error) {
	if err := plumbing.NewBranchReferenceName(cfg.Repo.Branch).Validate(); err != nil {
		return nil, fmt.Errorf("invalid branch name %q: %w", cfg.Repo.Branch, err)
	}
	if _, err := os.Stat(p.Marker); errors.Is(err, os.ErrNotExist) {
		return initRepo(ctx, cfg, p, log)
	} else if err != nil {
		return nil, err
	}
	return openRepo(cfg, p, log)
}

func newStorage(p Paths) *filesystem.Storage {
	return filesystem.NewStorage(osfs.New(p.GitDir, osfs.WithBoundOS()), cache.NewObjectLRUDefault())
}

func openGit(cfg *Config, p Paths, log *slog.Logger) (*Repo, error) {
	// The metadata lives in a separate directory and is opened with an
	// explicit work tree, so the work tree never contains a .git entry.
	gr, err := git.Open(newStorage(p), osfs.New(p.WorkTree, osfs.WithBoundOS()))
	if err != nil {
		return nil, err
	}
	wt, err := gr.Worktree()
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(p.WorkTree)
	if err != nil {
		return nil, err
	}
	return &Repo{cfg: cfg, paths: p, log: log, r: gr, wt: wt, root: root}, nil
}

func initRepo(ctx context.Context, cfg *Config, p Paths, log *slog.Logger) (*Repo, error) {
	// No marker means a previous initialization never completed. The
	// marker is written before the first mount, so these directories can
	// never contain user data and are safe to discard.
	for _, d := range []string{p.GitDir, p.WorkTree} {
		if err := os.RemoveAll(d); err != nil {
			return nil, err
		}
	}
	if err := os.Mkdir(p.GitDir, 0o700); err != nil {
		return nil, err
	}
	// This becomes the mount's root directory, so it gets normal
	// directory permissions; the 0700 state dir already keeps other
	// users away from the backing files.
	if err := os.Mkdir(p.WorkTree, 0o755); err != nil {
		return nil, err
	}
	log.Info("cloning", "url", cfg.Repo.URL, "branch", cfg.Repo.Branch)

	branch := plumbing.NewBranchReferenceName(cfg.Repo.Branch)
	// Initialized without a work tree: go-git would otherwise drop a
	// ".git" pointer file into it.
	gr, err := git.InitWithOptions(newStorage(p), nil, git.InitOptions{DefaultBranch: branch})
	if err != nil {
		return nil, err
	}
	gc, err := gr.Config()
	if err != nil {
		return nil, err
	}
	gc.Core.IsBare = false
	gc.Remotes[remoteName] = &config.RemoteConfig{
		Name:  remoteName,
		URLs:  []string{cfg.Repo.URL},
		Fetch: []config.RefSpec{config.RefSpec("+" + branch.String() + ":" + plumbing.NewRemoteReferenceName(remoteName, cfg.Repo.Branch).String())},
	}
	if err := gr.SetConfig(gc); err != nil {
		return nil, err
	}

	r, err := openGit(cfg, p, log)
	if err != nil {
		return nil, err
	}
	if err := r.Fetch(ctx); err != nil {
		r.Close()
		return nil, fmt.Errorf("initial fetch of branch %q: %w", cfg.Repo.Branch, err)
	}
	if err := r.checkoutInitial(); err != nil {
		r.Close()
		return nil, err
	}
	if err := writeFileSync(p.Marker, []byte("ok\n")); err != nil {
		r.Close()
		return nil, err
	}
	log.Info("clone complete")
	return r, nil
}

func (r *Repo) checkoutInitial() error {
	up, err := r.r.Reference(r.remoteRef(), true)
	if err != nil {
		return err
	}
	upC, err := r.r.CommitObject(up.Hash())
	if err != nil {
		return err
	}
	upT, err := upC.Tree()
	if err != nil {
		return err
	}
	files, err := changedPaths(nil, upT)
	if err != nil {
		return err
	}
	if err := r.apply(files, nil); err != nil {
		return err
	}
	if err := r.r.Storer.SetReference(plumbing.NewHashReference(r.branchRef(), up.Hash())); err != nil {
		return err
	}
	return r.wt.Reset(&git.ResetOptions{Commit: up.Hash(), Mode: git.MixedReset})
}

func openRepo(cfg *Config, p Paths, log *slog.Logger) (*Repo, error) {
	r, err := openGit(cfg, p, log)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Repo, error) { r.Close(); return nil, err }

	rc, err := r.r.Remote(remoteName)
	if err != nil {
		return fail(err)
	}
	if u := rc.Config().URLs; len(u) != 1 || u[0] != cfg.Repo.URL {
		return fail(fmt.Errorf("state dir %s belongs to %v, config says %q; use a different state.dir", p.Root, u, cfg.Repo.URL))
	}
	head, err := r.r.Reference(plumbing.HEAD, false)
	if err != nil {
		return fail(err)
	}
	if head.Type() != plumbing.SymbolicReference || head.Target() != r.branchRef() {
		return fail(fmt.Errorf("state dir %s has HEAD %v, config says branch %q; use a different state.dir", p.Root, head, cfg.Repo.Branch))
	}

	// Remove temp files left by a crash in the middle of apply. The FUSE
	// layer refuses this prefix, so they can only be gitmount's own.
	err = filepath.WalkDir(p.WorkTree, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), tmpPrefix) && !d.IsDir() {
			log.Warn("removing leftover temp file", "file", path)
			return os.Remove(path)
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}

	// A crash while the index was being written can leave it unreadable.
	// Rebuilding it from HEAD does not touch the work tree; any work tree
	// changes are then picked up by the next commit.
	if _, err := r.r.Storer.Index(); err != nil {
		log.Warn("index unreadable, rebuilding from HEAD", "err", err)
		// The index is only a cache of HEAD's tree here, so it is safe
		// to drop; go-git treats a missing index as empty.
		if err := os.Remove(filepath.Join(p.GitDir, "index")); err != nil {
			return fail(err)
		}
		h, err := r.r.Head()
		if err != nil {
			return fail(err)
		}
		if err := r.wt.Reset(&git.ResetOptions{Commit: h.Hash(), Mode: git.MixedReset}); err != nil {
			return fail(err)
		}
	}
	log.Info("reusing existing clone", "dir", p.Root)
	return r, nil
}

func (r *Repo) netCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, r.cfg.Sync.NetworkTimeout.Duration)
}

// Fetch updates refs/remotes/origin/<branch> only.
func (r *Repo) Fetch(ctx context.Context) error {
	auth, err := authMethod(r.cfg)
	if err != nil {
		return err
	}
	ca, err := caBundle(r.cfg)
	if err != nil {
		return err
	}
	ctx, cancel := r.netCtx(ctx)
	defer cancel()
	err = r.r.FetchContext(ctx, &git.FetchOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{r.fetchSpec()},
		Auth:       auth,
		Tags:       git.NoTags,
		CABundle:   ca,
	})
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return err
}

// Push sends HEAD to the remote branch. It is never forced: the remote
// rejects anything that is not a fast-forward.
func (r *Repo) Push(ctx context.Context) (plumbing.Hash, error) {
	auth, err := authMethod(r.cfg)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	ca, err := caBundle(r.cfg)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	head, err := r.r.Head()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	ctx, cancel := r.netCtx(ctx)
	defer cancel()
	b := r.branchRef().String()
	err = r.r.PushContext(ctx, &git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(b + ":" + b)},
		Auth:       auth,
		CABundle:   ca,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return plumbing.ZeroHash, err
	}
	return head.Hash(), r.r.Storer.SetReference(plumbing.NewHashReference(r.remoteRef(), head.Hash()))
}

func (r *Repo) commits() (head, up *object.Commit, err error) {
	h, err := r.r.Head()
	if err != nil {
		return nil, nil, err
	}
	u, err := r.r.Reference(r.remoteRef(), true)
	if err != nil {
		return nil, nil, err
	}
	if head, err = r.r.CommitObject(h.Hash()); err != nil {
		return nil, nil, err
	}
	if up, err = r.r.CommitObject(u.Hash()); err != nil {
		return nil, nil, err
	}
	return head, up, nil
}

func (r *Repo) Relation() (Relation, error) {
	head, up, err := r.commits()
	if err != nil {
		return 0, err
	}
	if head.Hash == up.Hash {
		return Equal, nil
	}
	if ok, err := up.IsAncestor(head); err != nil {
		return 0, err
	} else if ok {
		return Ahead, nil
	}
	if ok, err := head.IsAncestor(up); err != nil {
		return 0, err
	} else if ok {
		return Behind, nil
	}
	return Diverged, nil
}

// Commit records every change in the work tree (respecting .gitignore).
// Must be called with FUSE mutations blocked.
func (r *Repo) Commit(message string) (files int, err error) {
	st, err := r.wt.Status()
	if err != nil {
		return 0, err
	}
	if st.IsClean() {
		return 0, nil
	}
	names := make([]string, 0, len(st))
	for name := range st {
		names = append(names, name)
	}
	sort.Strings(names)

	if err := r.wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return 0, err
	}
	var b strings.Builder
	b.WriteString(message + "\n\n")
	for i, name := range names {
		if i == maxListedFiles {
			fmt.Fprintf(&b, "... and %d more\n", len(names)-maxListedFiles)
			break
		}
		fs := st[name]
		code := fs.Worktree
		if code == git.Unmodified {
			code = fs.Staging
		}
		if code == git.Untracked {
			code = git.Added
		}
		fmt.Fprintf(&b, "%c\t%s\n", code, name)
	}
	sig := &object.Signature{Name: r.cfg.Commit.AuthorName, Email: r.cfg.Commit.AuthorEmail, When: time.Now()}
	_, err = r.wt.Commit(b.String(), &git.CommitOptions{All: true, Author: sig, Committer: sig})
	if errors.Is(err, git.ErrEmptyCommit) {
		return 0, nil // e.g. only a permission change git does not track
	}
	if err != nil {
		return 0, err
	}
	return len(names), nil
}

// Integrate brings upstream changes into the work tree and puts local,
// unpushed changes on top of them as a single new commit (a path-level
// rebase). It refuses, without modifying anything, when both sides changed
// the same path differently. Must be called with FUSE mutations blocked
// and no file open for writing.
func (r *Repo) Integrate() (localFiles int, err error) {
	head, up, err := r.commits()
	if err != nil {
		return 0, err
	}
	bases, err := head.MergeBase(up)
	if err != nil {
		return 0, err
	}
	if len(bases) == 0 {
		return 0, &ConflictError{Reason: "local and remote branch share no history (was the remote force-pushed?)"}
	}
	trees := make([]*object.Tree, 3)
	for i, c := range []*object.Commit{bases[0], head, up} {
		if trees[i], err = c.Tree(); err != nil {
			return 0, err
		}
	}
	baseT, headT, upT := trees[0], trees[1], trees[2]

	ours, err := changedPaths(baseT, headT)
	if err != nil {
		return 0, err
	}
	theirs, err := changedPaths(baseT, upT)
	if err != nil {
		return 0, err
	}
	if c := findConflicts(ours, theirs); len(c) > 0 {
		return 0, &ConflictError{Reason: "changed both locally and upstream", Paths: c}
	}

	// Upstream changes that are already present locally need no work.
	pending := map[string]*entryState{}
	for p, st := range theirs {
		if o, ok := ours[p]; !ok || !o.equal(st) {
			pending[p] = st
		}
	}

	st, err := r.wt.Status()
	if err != nil {
		return 0, err
	}
	if !st.IsClean() {
		return 0, errors.New("work tree has uncommitted changes; will retry after the next commit")
	}

	if err := r.apply(pending, headT); err != nil {
		// A partial apply loses nothing: the caller marks the tree
		// dirty, the next commit records the work tree as it is, and
		// the next integration skips whatever already matches upstream.
		return 0, fmt.Errorf("applying upstream changes: %w", err)
	}
	if err := r.wt.Reset(&git.ResetOptions{Commit: up.Hash, Mode: git.MixedReset}); err != nil {
		return 0, err
	}
	if len(ours) == 0 {
		return 0, nil
	}
	return r.Commit(r.cfg.Commit.Message)
}

func writeFileSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
