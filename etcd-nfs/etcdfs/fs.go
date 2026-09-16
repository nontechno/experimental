// Package etcdfs implements a go-billy filesystem on top of an etcd v3
// keyspace, using "/" in keys as the directory delimiter.
//
// Key layout, relative to the root prefix:
//
//	a/b/c    regular file "a/b/c"; the file content is the key's value
//	a/b/     marker for directory "a/b" (empty value)
//
// Directories are also implicit: "a" and "a/b" exist while any key starts
// with "a/b/". Markers only keep empty directories alive. If both "a" and
// "a/..." exist, "a" is a directory and the value of "a" is shadowed. Keys
// with empty or dot segments ("a//b", "a/./b") are not listed.
//
// Every mutation is a single etcd transaction guarded by revision compares
// and retried with backoff, so concurrent writers never lose updates.
package etcdfs

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/go-git/go-billy/v5"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// Options configures an FS. Zero values select the defaults noted.
type Options struct {
	// Prefix is the etcd key prefix mapped to the filesystem root. It is
	// normalized to end with "/". Default "/".
	Prefix string

	FileMode os.FileMode // permission bits reported for files; default 0644
	DirMode  os.FileMode // permission bits reported for directories; default 0755
	UID, GID uint32      // owner reported for every entry

	// MaxFileSize caps a file's content (one etcd value). Default 1 MiB.
	MaxFileSize int
	// MaxRequestBytes must not exceed the server's --max-request-bytes.
	// It bounds writes and directory renames. Default 1.5 MiB.
	MaxRequestBytes int
	// MaxTxnOps must not exceed the server's --max-txn-ops. It bounds
	// directory renames and batched reads. Default 128.
	MaxTxnOps int

	ReadOnly       bool
	RequestTimeout time.Duration // per filesystem operation; default 10s

	// Deny, if set, rejects creating entries whose base name it matches.
	Deny func(name string) bool
}

const (
	defaultMaxFileSize     = 1 << 20
	defaultMaxRequestBytes = 3 << 19 // 1.5 MiB, etcd's default
	defaultMaxTxnOps       = 128
	defaultRequestTimeout  = 10 * time.Second
	clockLimit             = 1 << 16
	// requestOverhead is headroom for keys and protobuf framing.
	requestOverhead = 4 << 10
	// casRetries bounds optimistic-concurrency retries per operation.
	casRetries = 64
)

// ErrTooLarge is returned when an operation does not fit in one etcd
// transaction (see Options.MaxTxnOps and Options.MaxRequestBytes).
var ErrTooLarge = errors.New("operation exceeds etcd transaction limits")

// FS is a billy.Filesystem backed by etcd. It is safe for concurrent use.
type FS struct {
	cli   *clientv3.Client
	root  string // key prefix of this view; always ends with "/"
	base  string // path of this view below the top-level root ("" at the top)
	opts  *Options
	clock *revClock
}

var (
	_ billy.Filesystem = (*FS)(nil)
	_ billy.Change     = (*FS)(nil)
	_ billy.Capable    = (*FS)(nil)
)

// New returns a filesystem rooted at opts.Prefix. It performs one read to
// verify that etcd is reachable.
func New(cli *clientv3.Client, opts Options) (*FS, error) {
	o := opts
	if o.Prefix == "" {
		o.Prefix = "/"
	}
	if !strings.HasSuffix(o.Prefix, "/") {
		o.Prefix += "/"
	}
	if o.FileMode == 0 {
		o.FileMode = 0o644
	}
	if o.DirMode == 0 {
		o.DirMode = 0o755
	}
	if o.MaxFileSize <= 0 {
		o.MaxFileSize = defaultMaxFileSize
	}
	if o.MaxRequestBytes <= 0 {
		o.MaxRequestBytes = defaultMaxRequestBytes
	}
	if o.MaxTxnOps <= 0 {
		o.MaxTxnOps = defaultMaxTxnOps
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = defaultRequestTimeout
	}
	switch {
	case o.MaxTxnOps < 8:
		return nil, fmt.Errorf("etcdfs: MaxTxnOps must be at least 8, got %d", o.MaxTxnOps)
	case o.MaxFileSize+requestOverhead > o.MaxRequestBytes:
		return nil, fmt.Errorf("etcdfs: MaxFileSize (%d) must be at least %d bytes below MaxRequestBytes (%d)",
			o.MaxFileSize, requestOverhead, o.MaxRequestBytes)
	}

	f := &FS{cli: cli, root: o.Prefix, opts: &o, clock: newRevClock(clockLimit)}
	ctx, cancel := f.ctx()
	defer cancel()
	resp, err := cli.Get(ctx, f.root, clientv3.WithCountOnly())
	if err != nil {
		return nil, fmt.Errorf("etcdfs: %w", err)
	}
	f.clock.observe(resp.Header.Revision) // pre-existing keys map to "now"
	return f, nil
}

// Root returns the etcd key prefix this view is rooted at.
func (f *FS) Root() string { return f.root }

// Base returns this view's path below the top-level root ("" at the top).
func (f *FS) Base() string { return f.base }

// Client returns the etcd client.
func (f *FS) Client() *clientv3.Client { return f.cli }

// Chroot returns a view rooted at the directory p. Views share the client,
// options and clock.
func (f *FS) Chroot(p string) (billy.Filesystem, error) {
	rel := cleanRel(p)
	if rel == "" {
		return f, nil
	}
	return &FS{cli: f.cli, root: f.dirKey(rel), base: f.absRel(rel), opts: f.opts, clock: f.clock}, nil
}

func (f *FS) Join(elem ...string) string { return path.Join(elem...) }

func (f *FS) Stat(filename string) (os.FileInfo, error) {
	ctx, cancel := f.ctx()
	defer cancel()
	e, err := f.lookup(ctx, cleanRel(filename))
	if err != nil {
		return nil, pathErr("stat", filename, err)
	}
	if !e.exists {
		return nil, pathErr("stat", filename, os.ErrNotExist)
	}
	return f.info(e), nil
}

// Lstat is Stat: etcd keys cannot be symlinks.
func (f *FS) Lstat(filename string) (os.FileInfo, error) { return f.Stat(filename) }

func (f *FS) Symlink(_, link string) error { return pathErr("symlink", link, syscall.ENOTSUP) }

func (f *FS) Readlink(link string) (string, error) {
	return "", pathErr("readlink", link, syscall.EINVAL)
}

// Chmod, Chown, Lchown and Chtimes are accepted so that cp, touch and editors
// work, but etcd has nowhere to keep ownership, modes or times.
func (f *FS) Chmod(string, os.FileMode) error            { return f.checkWritable() }
func (f *FS) Chown(string, int, int) error               { return f.checkWritable() }
func (f *FS) Lchown(string, int, int) error              { return f.checkWritable() }
func (f *FS) Chtimes(string, time.Time, time.Time) error { return f.checkWritable() }

func (f *FS) Capabilities() billy.Capability {
	if f.opts.ReadOnly {
		return billy.ReadCapability | billy.SeekCapability
	}
	return billy.WriteCapability | billy.ReadCapability | billy.ReadAndWriteCapability |
		billy.SeekCapability | billy.TruncateCapability
}

// ---------------------------------------------------------------------------
// internals

// entry is the result of resolving a path.
type entry struct {
	rel       string
	exists    bool
	isDir     bool
	kv        *mvccpb.KeyValue // file key; nil for directories
	headerRev int64            // store revision of the read
}

// lookup resolves rel in one read-only transaction: the file key plus the
// first key under the directory prefix.
func (f *FS) lookup(ctx context.Context, rel string) (*entry, error) {
	if rel == "" {
		resp, err := f.cli.Get(ctx, f.root, clientv3.WithCountOnly())
		if err != nil {
			return nil, err
		}
		f.clock.observe(resp.Header.Revision)
		return &entry{exists: true, isDir: true, headerRev: resp.Header.Revision}, nil
	}
	resp, err := f.cli.Txn(ctx).Then(
		clientv3.OpGet(f.fileKey(rel)),
		clientv3.OpGet(f.dirKey(rel), clientv3.WithPrefix(), clientv3.WithLimit(1), clientv3.WithKeysOnly()),
	).Commit()
	if err != nil {
		return nil, err
	}
	f.clock.observe(resp.Header.Revision)
	e := &entry{rel: rel, headerRev: resp.Header.Revision}
	switch file, dir := resp.Responses[0].GetResponseRange(), resp.Responses[1].GetResponseRange(); {
	case len(dir.Kvs) > 0:
		e.exists, e.isDir = true, true
	case len(file.Kvs) > 0:
		e.exists, e.kv = true, file.Kvs[0]
	}
	return e, nil
}

func (f *FS) info(e *entry) os.FileInfo {
	if e.isDir {
		return f.newDirInfo(f.absRel(e.rel), e.headerRev)
	}
	return f.newFileInfo(f.absRel(e.rel), int64(len(e.kv.Value)), e.kv.ModRevision, e.headerRev)
}

// requireDir ensures rel exists and is a directory.
func (f *FS) requireDir(ctx context.Context, op, name, rel string) error {
	e, err := f.lookup(ctx, rel)
	switch {
	case err != nil:
		return pathErr(op, name, err)
	case !e.exists:
		return pathErr(op, name, os.ErrNotExist)
	case !e.isDir:
		return pathErr(op, name, syscall.ENOTDIR)
	}
	return nil
}

// keepDir returns an op that writes the marker of directory rel if it is
// missing, so that removing or moving the last entry out of an implicit
// directory does not make the directory vanish under the client.
func (f *FS) keepDir(rel string) []clientv3.Op {
	if rel == "" {
		return nil
	}
	dk := f.dirKey(rel)
	return []clientv3.Op{clientv3.OpTxn(
		[]clientv3.Cmp{clientv3.Compare(clientv3.CreateRevision(dk), "=", 0)},
		[]clientv3.Op{clientv3.OpPut(dk, "")},
		nil,
	)}
}

// commit runs a transaction and records the resulting revision.
func (f *FS) commit(ctx context.Context, cmps []clientv3.Cmp, ops ...clientv3.Op) (bool, error) {
	resp, err := f.cli.Txn(ctx).If(cmps...).Then(ops...).Commit()
	if err != nil {
		return false, err
	}
	f.clock.observe(resp.Header.Revision)
	return resp.Succeeded, nil
}

func (f *FS) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), f.opts.RequestTimeout)
}

func (f *FS) checkWritable() error {
	if f.opts.ReadOnly {
		return syscall.EROFS
	}
	return nil
}

func (f *FS) denied(rel string) bool {
	return f.opts.Deny != nil && f.opts.Deny(baseName(rel))
}

// withLease keeps a key's lease when it is rewritten.
func withLease(kv *mvccpb.KeyValue) []clientv3.OpOption {
	if kv == nil || kv.Lease == 0 {
		return nil
	}
	return []clientv3.OpOption{clientv3.WithLease(clientv3.LeaseID(kv.Lease))}
}

// retry runs attempt until it reports done, backing off with jitter between
// attempts so contending writers converge instead of livelocking.
func retry(ctx context.Context, op, name string, attempt func() (done bool, err error)) error {
	for i := 0; i < casRetries; i++ {
		if i > 0 {
			d := time.Duration(1<<min(i, 6)) * time.Millisecond
			d = d/2 + rand.N(d/2+1)
			select {
			case <-ctx.Done():
				return pathErr(op, name, ctx.Err())
			case <-time.After(d):
			}
		}
		done, err := attempt()
		if err != nil || done {
			return err
		}
	}
	return pathErr(op, name, syscall.EAGAIN)
}

// pathErr wraps err as an *os.PathError unless it already is one.
func pathErr(op, name string, err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return err
	}
	return &os.PathError{Op: op, Path: name, Err: err}
}
