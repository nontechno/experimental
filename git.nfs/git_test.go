package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestNFSFilesystemMutationCommitsAndPushes(t *testing.T) {
	root := t.TempDir()
	remotePath := filepath.Join(root, "remote.git")
	remote, err := git.PlainInit(remotePath, true)
	if err != nil {
		t.Fatal(err)
	}
	seedPath := filepath.Join(root, "seed")
	seed, err := git.PlainInit(seedPath, false)
	if err != nil {
		t.Fatal(err)
	}
	seedTree, err := seed.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedPath, "README"), []byte("seed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTree.Add("README"); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTree.Commit("seed", &git.CommitOptions{Author: &object.Signature{Name: "Seed", Email: "seed@example.invalid", When: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{remotePath}}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Push(&git.PushOptions{RemoteName: "origin"}); err != nil {
		t.Fatal(err)
	}
	_ = remote

	workPath := filepath.Join(root, "checkout")
	gitPath := filepath.Join(root, "metadata")
	cfg := Config{
		Repository: remotePath, Branch: "master", WorkDirectory: workPath, GitDirectory: gitPath,
		Commit: Commit{Name: "Git NFS", Email: "gitnfs@example.invalid", Message: "NFS write"},
	}
	g, err := openGit(cfg)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	fs := &exportFS{Filesystem: osfs.New(workPath, osfs.WithBoundOS()), repo: g}
	entries, err := fs.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			t.Fatal("Git metadata pointer must not be exported")
		}
	}
	if _, err := fs.Stat(".git"); err == nil {
		t.Fatal("Git metadata pointer is unexpectedly accessible")
	}
	file, err := fs.Create("hello.txt")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := file.Write([]byte("through NFS\n")); err != nil {
		t.Fatalf("write and push: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	ignore, err := fs.Create(".gitignore")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ignore.Write([]byte("*.ignored\n")); err != nil {
		t.Fatal(err)
	}
	if err := ignore.Close(); err != nil {
		t.Fatal(err)
	}
	ignoredByGit, err := fs.Create("visible.ignored")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ignoredByGit.Write([]byte("still part of the mounted tree\n")); err != nil {
		t.Fatal(err)
	}
	if err := ignoredByGit.Close(); err != nil {
		t.Fatal(err)
	}

	ref, err := remote.Reference(plumbing.NewBranchReferenceName("master"), true)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := remote.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	contents, err := commit.File("hello.txt")
	if err != nil {
		t.Fatalf("remote commit has no file: %v", err)
	}
	r, err := contents.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data := make([]byte, 32)
	n, err := r.Read(data)
	if err != nil && n == 0 {
		t.Fatal(err)
	}
	if got, want := string(data[:n]), "through NFS\n"; got != want {
		t.Fatalf("remote file = %q, want %q", got, want)
	}
	if _, err := commit.File("visible.ignored"); err != nil {
		t.Fatalf("visible ignored file was not pushed: %v", err)
	}
}
