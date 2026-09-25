package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	billy "github.com/go-git/go-billy/v5"
)

// exportFS makes each NFS mutation a serialized Git commit and fast-forward push.
// Symlinks are deliberately unsupported because they can escape the export root.
type exportFS struct {
	billy.Filesystem
	repo *gitRepo
	mu   sync.Mutex
}

func (f *exportFS) mutate(op func() error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := op(); err != nil {
		return err
	}
	return f.repo.sync()
}

func (f *exportFS) Create(path string) (billy.File, error) {
	if err := checkExportPath(path); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	file, err := f.Filesystem.Create(path)
	if err != nil {
		return nil, err
	}
	if err := f.repo.sync(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &exportFile{File: file, parent: f}, nil
}

func (f *exportFS) OpenFile(path string, flag int, perm os.FileMode) (billy.File, error) {
	if err := checkExportPath(path); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	file, err := f.Filesystem.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_TRUNC|os.O_APPEND) != 0 {
		if err := f.repo.sync(); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return &exportFile{File: file, parent: f}, nil
}

func (f *exportFS) Open(path string) (billy.File, error) { return f.OpenFile(path, os.O_RDONLY, 0) }
func (f *exportFS) Stat(path string) (os.FileInfo, error) {
	if err := checkExportPath(path); err != nil {
		return nil, err
	}
	return f.Filesystem.Stat(path)
}
func (f *exportFS) Lstat(path string) (os.FileInfo, error) {
	if err := checkExportPath(path); err != nil {
		return nil, err
	}
	return f.Filesystem.Lstat(path)
}

func (f *exportFS) Rename(oldpath, newpath string) error {
	if err := checkExportPath(oldpath); err != nil {
		return err
	}
	if err := checkExportPath(newpath); err != nil {
		return err
	}
	return f.mutate(func() error { return f.Filesystem.Rename(oldpath, newpath) })
}
func (f *exportFS) Remove(path string) error {
	if err := checkExportPath(path); err != nil {
		return err
	}
	return f.mutate(func() error { return f.Filesystem.Remove(path) })
}
func (f *exportFS) MkdirAll(path string, perm os.FileMode) error {
	if err := checkExportPath(path); err != nil {
		return err
	}
	return f.mutate(func() error { return f.Filesystem.MkdirAll(path, perm) })
}
func (f *exportFS) ReadDir(path string) ([]os.FileInfo, error) {
	if err := checkExportPath(path); err != nil {
		return nil, err
	}
	entries, err := f.Filesystem.ReadDir(path)
	if err != nil {
		return nil, err
	}
	filtered := entries[:0]
	for _, entry := range entries {
		if entry.Name() != ".git" {
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
}
func (f *exportFS) Symlink(target, link string) error {
	return errors.New("symbolic links are disabled")
}

func (f *exportFS) Chmod(path string, mode os.FileMode) error {
	if err := checkExportPath(path); err != nil {
		return err
	}
	ch, ok := f.Filesystem.(billy.Chmod)
	if !ok {
		return billy.ErrNotSupported
	}
	return f.mutate(func() error { return ch.Chmod(path, mode) })
}
func (f *exportFS) Lchown(string, int, int) error { return billy.ErrNotSupported }
func (f *exportFS) Chown(string, int, int) error  { return billy.ErrNotSupported }
func (f *exportFS) Chtimes(path string, atime, mtime time.Time) error {
	if err := checkExportPath(path); err != nil {
		return err
	}
	ch, ok := f.Filesystem.(billy.Change)
	if !ok {
		return billy.ErrNotSupported
	}
	return f.mutate(func() error { return ch.Chtimes(path, atime, mtime) })
}

func (f *exportFS) TempFile(dir, prefix string) (billy.File, error) {
	if dir != "" {
		if err := checkExportPath(dir); err != nil {
			return nil, err
		}
	}
	return nil, billy.ErrNotSupported
}
func (f *exportFS) Readlink(path string) (string, error) {
	if err := checkExportPath(path); err != nil {
		return "", err
	}
	return "", billy.ErrNotSupported
}

func checkExportPath(path string) error {
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(path)), "/") {
		if part == ".git" {
			return os.ErrPermission
		}
	}
	return nil
}

type exportFile struct {
	billy.File
	parent *exportFS
}

func (f *exportFile) Write(p []byte) (int, error) {
	parent := f.parent
	parent.mu.Lock()
	defer parent.mu.Unlock()
	n, err := f.File.Write(p)
	if n > 0 {
		if syncer, ok := f.File.(interface{ Sync() error }); ok {
			if syncErr := syncer.Sync(); syncErr != nil && err == nil {
				return n, syncErr
			}
		}
		if syncErr := parent.repo.sync(); syncErr != nil && err == nil {
			err = syncErr
		}
	}
	return n, err
}
