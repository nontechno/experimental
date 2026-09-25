package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

type gitRepo struct {
	repo *git.Repository
	work *git.Worktree
	cfg  Config
	auth transport.AuthMethod
}

func openGit(cfg Config) (*gitRepo, error) {
	if err := os.MkdirAll(cfg.WorkDirectory, 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.GitDirectory, 0700); err != nil {
		return nil, err
	}
	workRoot, err := filepath.EvalSymlinks(cfg.WorkDirectory)
	if err != nil {
		return nil, err
	}
	gitRoot, err := filepath.EvalSymlinks(cfg.GitDirectory)
	if err != nil {
		return nil, err
	}
	if workRoot == gitRoot || within(workRoot, gitRoot) || within(gitRoot, workRoot) {
		return nil, errors.New("work_directory and git_directory must be separate, non-nested directories after resolving symlinks")
	}
	wfs := osfs.New(cfg.WorkDirectory, osfs.WithBoundOS())
	sfs := filesystem.NewStorage(osfs.New(cfg.GitDirectory, osfs.WithBoundOS()), cache.NewObjectLRUDefault())
	auth, err := gitAuth(cfg.Auth)
	if err != nil {
		return nil, err
	}
	var repo *git.Repository
	if _, err := os.Stat(filepath.Join(cfg.GitDirectory, "config")); errors.Is(err, os.ErrNotExist) {
		entries, readErr := os.ReadDir(cfg.WorkDirectory)
		if readErr != nil {
			return nil, readErr
		}
		if len(entries) != 0 {
			return nil, errors.New("work_directory must be empty when git_directory has no repository")
		}
		gitEntries, readErr := os.ReadDir(cfg.GitDirectory)
		if readErr != nil {
			return nil, readErr
		}
		if len(gitEntries) != 0 {
			return nil, errors.New("git_directory must be empty when creating a repository")
		}
		repo, err = git.Clone(sfs, wfs, &git.CloneOptions{
			URL: cfg.Repository, Auth: auth, ReferenceName: plumbing.NewBranchReferenceName(cfg.Branch),
			SingleBranch: true, Tags: git.NoTags,
		})
		if err != nil {
			return nil, fmt.Errorf("clone remote repository: %w", err)
		}
	} else if err != nil {
		return nil, err
	} else {
		repo, err = git.Open(sfs, wfs)
		if err != nil {
			return nil, fmt.Errorf("open git repository: %w", err)
		}
		remote, err := repo.Remote("origin")
		if err != nil {
			return nil, fmt.Errorf("existing git_directory has no origin remote: %w", err)
		}
		if len(remote.Config().URLs) != 1 || remote.Config().URLs[0] != cfg.Repository {
			return nil, errors.New("configured repository does not match origin in existing git_directory")
		}
		head, err := repo.Reference(plumbing.HEAD, false)
		if err != nil {
			return nil, err
		}
		branchRef := plumbing.NewBranchReferenceName(cfg.Branch)
		if head.Type() != plumbing.SymbolicReference || head.Target() != branchRef {
			return nil, fmt.Errorf("existing git_directory is checked out on %q, not configured branch %q", head.Target(), cfg.Branch)
		}
		_, err = repo.Reference(branchRef, true)
		if err != nil {
			return nil, fmt.Errorf("configured branch %q is missing from existing git_directory: %w", cfg.Branch, err)
		}
	}
	if err != nil {
		return nil, err
	}
	work, err := repo.Worktree()
	if err != nil {
		return nil, err
	}
	// A checked-out Git symlink is an OS symlink. Reject it before exporting the tree.
	if err := rejectSymlinks(cfg.WorkDirectory); err != nil {
		return nil, err
	}
	g := &gitRepo{repo: repo, work: work, cfg: cfg, auth: auth}
	if err := g.sync(); err != nil {
		return nil, fmt.Errorf("sync existing checkout: %w", err)
	}
	return g, nil
}

func gitAuth(c GitAuth) (transport.AuthMethod, error) {
	switch c.Type {
	case "", "none":
		return nil, nil
	case "https":
		if c.Token != "" {
			return &http.BasicAuth{Username: "git", Password: c.Token}, nil
		}
		return &http.BasicAuth{Username: c.Username, Password: c.Password}, nil
	case "ssh":
		key, err := os.ReadFile(c.PrivateKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read ssh private key: %w", err)
		}
		user := c.SSHUser
		if user == "" {
			user = "git"
		}
		hostKeyCallback, err := ssh.NewKnownHostsCallback(c.KnownHostsFile)
		if err != nil {
			return nil, fmt.Errorf("load SSH known hosts: %w", err)
		}
		if c.Passphrase != "" {
			method, err := ssh.NewPublicKeys(user, key, c.Passphrase)
			if err != nil {
				return nil, err
			}
			method.HostKeyCallback = hostKeyCallback
			return method, nil
		}
		method, err := ssh.NewPublicKeys(user, key, "")
		if err != nil {
			return nil, err
		}
		method.HostKeyCallback = hostKeyCallback
		return method, nil
	default:
		return nil, fmt.Errorf("unsupported auth type %q", c.Type)
	}
}

func rejectSymlinks(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("repository contains symbolic link %q; refusing to export it", path)
		}
		return nil
	})
}

// sync commits the current working tree and pushes a fast-forward update.
// The caller serializes filesystem mutations and this operation together.
func (g *gitRepo) sync() error {
	if err := g.stageAll(); err != nil {
		return fmt.Errorf("stage working tree: %w", err)
	}
	status, err := g.work.Status()
	if err != nil {
		return err
	}
	if status.IsClean() {
		return nil
	}
	message := g.cfg.Commit.Message
	if message == "" {
		message = "Update via gitnfs"
	}
	_, err = g.work.Commit(message, &git.CommitOptions{Author: &object.Signature{
		Name: g.cfg.Commit.Name, Email: g.cfg.Commit.Email, When: time.Now(),
	}})
	if err != nil {
		return fmt.Errorf("commit filesystem change: %w", err)
	}
	refspec := config.RefSpec("refs/heads/" + g.cfg.Branch + ":refs/heads/" + g.cfg.Branch)
	if err := g.repo.Push(&git.PushOptions{Auth: g.auth, RefSpecs: []config.RefSpec{refspec}}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("push commit to remote: %w", err)
	}
	return nil
}

// stageAll intentionally includes files matched by .gitignore: in a mounted
// filesystem every visible regular file must be represented in the pushed tree.
func (g *gitRepo) stageAll() error {
	idx, err := g.repo.Storer.Index()
	if err != nil {
		return err
	}
	tracked := make(map[string]struct{}, len(idx.Entries))
	for _, entry := range idx.Entries {
		tracked[entry.Name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(tracked))
	err = filepath.WalkDir(g.cfg.WorkDirectory, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == g.cfg.WorkDirectory {
			return nil
		}
		if path == filepath.Join(g.cfg.WorkDirectory, ".git") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil // go-git stores a metadata pointer here; never add/export it.
		}
		rel, err := filepath.Rel(g.cfg.WorkDirectory, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic link %q is not supported", rel)
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file %q is not supported", rel)
		}
		if err := g.work.AddWithOptions(&git.AddOptions{Path: rel, SkipStatus: true}); err != nil {
			return err
		}
		seen[rel] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	for path := range tracked {
		if _, ok := seen[path]; ok {
			continue
		}
		if _, err := g.work.Filesystem.Lstat(path); err == nil {
			// A tracked file may have been replaced by a directory.
			info, statErr := g.work.Filesystem.Lstat(path)
			if statErr != nil || !info.IsDir() {
				continue
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := g.work.AddWithOptions(&git.AddOptions{Path: path, SkipStatus: true}); err != nil {
			return err
		}
	}
	return nil
}
