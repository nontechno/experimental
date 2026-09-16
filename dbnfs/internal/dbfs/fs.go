package dbfs

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/go-git/go-billy/v5"
)

// Options configure an FS.
type Options struct {
	CacheTTL     time.Duration // how long listings and files are reused
	ErrorTTL     time.Duration // how long a file holding an error message is reused
	QueryTimeout time.Duration // deadline for each Source call
	MaxFileBytes int           // hard cap on generated file size
	CacheBytes   int64         // approximate memory budget for cached content
	Logger       *slog.Logger
}

// FS is a read-only billy.Filesystem backed by a Source.
type FS struct {
	src       Source
	opts      Options
	kinds     map[string]Kind
	kindOrder []string
	schemaSet map[string]bool
	cache     *cache
	started   time.Time
	readme    []byte
	log       *slog.Logger
}

var _ billy.Filesystem = (*FS)(nil)
var _ billy.Capable = (*FS)(nil)

const (
	readmeName     = "README.txt"
	noIndexMarker  = ".metadata_never_index" // asks macOS Spotlight not to crawl the volume
	dirMode        = fs.ModeDir | 0o555
	fileMode       = fs.FileMode(0o444)
	defaultErrTTL  = 5 * time.Second
	defaultCacheMB = 256
)

// New builds an FS. It panics if the Source declares an invalid layout,
// which is a programming error.
func New(src Source, opts Options) *FS {
	if opts.ErrorTTL <= 0 {
		opts.ErrorTTL = defaultErrTTL
	}
	if opts.CacheBytes <= 0 {
		opts.CacheBytes = defaultCacheMB << 20
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	f := &FS{
		src:       src,
		opts:      opts,
		kinds:     map[string]Kind{},
		schemaSet: map[string]bool{},
		cache:     newCache(opts.CacheBytes),
		started:   time.Now(),
		log:       opts.Logger,
	}
	for _, k := range src.Kinds() {
		if k.Dir == "" || k.Dir == SchemaDir || k.Dir == readmeName || strings.ContainsAny(k.Dir, "/.") {
			panic(fmt.Sprintf("dbfs: invalid kind directory %q", k.Dir))
		}
		if _, dup := f.kinds[k.Dir]; dup {
			panic(fmt.Sprintf("dbfs: duplicate kind %q", k.Dir))
		}
		f.kinds[k.Dir] = k
		f.kindOrder = append(f.kindOrder, k.Dir)
	}
	for _, name := range src.SchemaFiles() {
		f.schemaSet[name] = true
	}
	f.readme = f.buildReadme()
	return f
}

func (f *FS) buildReadme() []byte {
	var b strings.Builder
	b.WriteString("dbnfs: read-only view of database catalog information.\n\n")
	b.WriteString("Directory names are OWNER.NAME. Characters that cannot appear in a file name\n")
	b.WriteString("are percent-encoded: '%' -> %25, '/' -> %2F, '.' -> %2E.\n")
	b.WriteString("Content is generated on first access and cached for ")
	b.WriteString(f.opts.CacheTTL.String())
	b.WriteString(".\nFiles that could not be generated contain the error message instead.\n\n")
	fmt.Fprintf(&b, "/%s/<OWNER>/\n", SchemaDir)
	for _, name := range f.src.SchemaFiles() {
		fmt.Fprintf(&b, "    %s\n", name)
	}
	for _, dir := range f.kindOrder {
		k := f.kinds[dir]
		fmt.Fprintf(&b, "/%s/<OWNER>.<NAME>/    %s\n", k.Dir, k.Description)
		for _, name := range k.Files {
			fmt.Fprintf(&b, "    %s\n", name)
		}
	}
	return []byte(b.String())
}

// ---- path resolution ----

type node struct {
	info    fileInfo
	content []byte // files only
	// directory listing function; nil for files
	list func() ([]os.FileInfo, error)
}

type fileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
}

func (i fileInfo) Name() string       { return i.name }
func (i fileInfo) Size() int64        { return i.size }
func (i fileInfo) Mode() fs.FileMode  { return i.mode }
func (i fileInfo) ModTime() time.Time { return i.modTime }
func (i fileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fileInfo) Sys() any           { return nil }

func dirInfo(name string, t time.Time) fileInfo {
	return fileInfo{name: name, mode: dirMode, modTime: t}
}

func fileInfoFor(name string, content []byte, t time.Time) fileInfo {
	return fileInfo{name: name, size: int64(len(content)), mode: fileMode, modTime: t}
}

func splitPath(p string) []string {
	p = path.Clean("/" + p)
	if p == "/" {
		return nil
	}
	return strings.Split(p[1:], "/")
}

func notExist(op, p string) error {
	return &os.PathError{Op: op, Path: p, Err: fs.ErrNotExist}
}

func readOnly(op, p string) error {
	return &os.PathError{Op: op, Path: p, Err: billy.ErrReadOnly}
}

// resolve maps a path to a node, validating every component against the
// catalog so that only existing objects have file handles.
func (f *FS) resolve(op, p string) (*node, error) {
	parts := splitPath(p)
	switch len(parts) {
	case 0:
		return &node{info: dirInfo("/", f.started), list: f.listRoot}, nil

	case 1:
		switch name := parts[0]; {
		case name == readmeName:
			return &node{info: fileInfoFor(name, f.readme, f.started), content: f.readme}, nil
		case name == noIndexMarker:
			return &node{info: fileInfoFor(name, nil, f.started), content: []byte{}}, nil
		case name == SchemaDir:
			return &node{info: dirInfo(name, f.started), list: f.listSchemas}, nil
		default:
			if _, ok := f.kinds[name]; ok {
				return &node{info: dirInfo(name, f.started), list: func() ([]os.FileInfo, error) { return f.listKind(name) }}, nil
			}
		}
		return nil, notExist(op, p)

	case 2, 3:
		if parts[0] == SchemaDir {
			return f.resolveSchema(op, p, parts)
		}
		if kind, ok := f.kinds[parts[0]]; ok {
			return f.resolveObject(op, p, kind, parts)
		}
	}
	return nil, notExist(op, p)
}

func (f *FS) resolveSchema(op, p string, parts []string) (*node, error) {
	owner, ok := decodeComponent(parts[1])
	if !ok {
		return nil, notExist(op, p)
	}
	set, loaded, err := f.schemaListing()
	if err != nil {
		return nil, err
	}
	if !set.has[owner] {
		return nil, notExist(op, p)
	}
	if len(parts) == 2 {
		return &node{
			info: dirInfo(parts[1], loaded),
			list: func() ([]os.FileInfo, error) { return f.listFiles(p, f.src.SchemaFiles(), f.schemaFileGen(owner)) },
		}, nil
	}
	if !f.schemaSet[parts[2]] {
		return nil, notExist(op, p)
	}
	content, t := f.fileContent(p, func(ctx context.Context, limit int) ([]byte, error) {
		return f.src.SchemaFile(ctx, owner, parts[2], limit)
	})
	return &node{info: fileInfoFor(parts[2], content, t), content: content}, nil
}

func (f *FS) resolveObject(op, p string, kind Kind, parts []string) (*node, error) {
	obj, ok := parseObjectEntryName(parts[1])
	if !ok {
		return nil, notExist(op, p)
	}
	set, loaded, err := f.objectListing(kind.Dir)
	if err != nil {
		return nil, err
	}
	if !set.has[parts[1]] {
		return nil, notExist(op, p)
	}
	if len(parts) == 2 {
		return &node{
			info: dirInfo(parts[1], loaded),
			list: func() ([]os.FileInfo, error) { return f.listFiles(p, kind.Files, f.objectFileGen(kind.Dir, obj)) },
		}, nil
	}
	if !contains(kind.Files, parts[2]) {
		return nil, notExist(op, p)
	}
	content, t := f.fileContent(p, func(ctx context.Context, limit int) ([]byte, error) {
		return f.src.ObjectFile(ctx, kind.Dir, obj, parts[2], limit)
	})
	return &node{info: fileInfoFor(parts[2], content, t), content: content}, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ---- listings ----

type nameSet struct {
	names []string // entry names, sorted
	has   map[string]bool
}

func newNameSet(names []string) *nameSet {
	s := &nameSet{has: make(map[string]bool, len(names))}
	for _, n := range names {
		if !s.has[n] {
			s.has[n] = true
			s.names = append(s.names, n)
		}
	}
	sort.Strings(s.names)
	return s
}

func (f *FS) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), f.opts.QueryTimeout)
}

func (f *FS) schemaListing() (*nameSet, time.Time, error) {
	v, t, err := f.cache.get("list\x00"+SchemaDir, func() (any, int64, time.Duration, error) {
		ctx, cancel := f.ctx()
		defer cancel()
		owners, err := f.src.Schemas(ctx)
		if err != nil {
			f.log.Error("listing schemas failed", "err", err)
			return nil, 0, 0, fmt.Errorf("listing schemas: %w", err)
		}
		// Keys are raw owner names; listing shows encoded names.
		s := newNameSet(owners)
		return s, sizeOf(s.names), f.opts.CacheTTL, nil
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	return v.(*nameSet), t, nil
}

func (f *FS) objectListing(kind string) (*nameSet, time.Time, error) {
	v, t, err := f.cache.get("list\x00"+kind, func() (any, int64, time.Duration, error) {
		ctx, cancel := f.ctx()
		defer cancel()
		objs, err := f.src.Objects(ctx, kind)
		if err != nil {
			f.log.Error("listing objects failed", "kind", kind, "err", err)
			return nil, 0, 0, fmt.Errorf("listing %s: %w", kind, err)
		}
		names := make([]string, len(objs))
		for i, o := range objs {
			names[i] = objectEntryName(o)
		}
		s := newNameSet(names)
		return s, sizeOf(s.names), f.opts.CacheTTL, nil
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	return v.(*nameSet), t, nil
}

func sizeOf(names []string) int64 {
	n := int64(0)
	for _, s := range names {
		n += int64(len(s))*2 + 64 // name in slice and map, plus overhead
	}
	return n
}

func (f *FS) listRoot() ([]os.FileInfo, error) {
	out := []os.FileInfo{
		fileInfoFor(readmeName, f.readme, f.started),
		dirInfo(SchemaDir, f.started),
	}
	for _, dir := range f.kindOrder {
		out = append(out, dirInfo(dir, f.started))
	}
	return out, nil
}

func (f *FS) listSchemas() ([]os.FileInfo, error) {
	set, t, err := f.schemaListing()
	if err != nil {
		return nil, err
	}
	out := make([]os.FileInfo, 0, len(set.names))
	for _, owner := range set.names {
		out = append(out, dirInfo(encodeComponent(owner), t))
	}
	return out, nil
}

func (f *FS) listKind(kind string) ([]os.FileInfo, error) {
	set, t, err := f.objectListing(kind)
	if err != nil {
		return nil, err
	}
	out := make([]os.FileInfo, 0, len(set.names))
	for _, name := range set.names {
		out = append(out, dirInfo(name, t))
	}
	return out, nil
}

type genFunc func(file string) func(ctx context.Context, limit int) ([]byte, error)

func (f *FS) schemaFileGen(owner string) genFunc {
	return func(file string) func(context.Context, int) ([]byte, error) {
		return func(ctx context.Context, limit int) ([]byte, error) {
			return f.src.SchemaFile(ctx, owner, file, limit)
		}
	}
}

func (f *FS) objectFileGen(kind string, obj Object) genFunc {
	return func(file string) func(context.Context, int) ([]byte, error) {
		return func(ctx context.Context, limit int) ([]byte, error) {
			return f.src.ObjectFile(ctx, kind, obj, file, limit)
		}
	}
}

// listFiles lists a directory of generated files. Sizes must be exact
// (READDIRPLUS returns them as attributes), so each file is generated.
func (f *FS) listFiles(dir string, files []string, gen genFunc) ([]os.FileInfo, error) {
	out := make([]os.FileInfo, 0, len(files))
	for _, name := range files {
		content, t := f.fileContent(path.Join(dir, name), gen(name))
		out = append(out, fileInfoFor(name, content, t))
	}
	return out, nil
}

// fileContent returns cached or freshly generated content. Generation
// errors become the file content (cached briefly), so one failing file
// never breaks listing its directory.
func (f *FS) fileContent(p string, gen func(ctx context.Context, limit int) ([]byte, error)) ([]byte, time.Time) {
	key := "file\x00" + path.Clean("/"+p)
	v, t, _ := f.cache.get(key, func() (any, int64, time.Duration, error) {
		ctx, cancel := f.ctx()
		defer cancel()
		start := time.Now()
		b, err := gen(ctx, f.opts.MaxFileBytes)
		if err != nil {
			f.log.Warn("generating file failed", "path", p, "err", err)
			b = errorContent(p, err)
			return b, int64(len(b)), f.opts.ErrorTTL, nil
		}
		if len(b) > f.opts.MaxFileBytes {
			b = b[:f.opts.MaxFileBytes]
		}
		f.log.Debug("generated file", "path", p, "bytes", len(b), "took", time.Since(start))
		return b, int64(len(b)), f.opts.CacheTTL, nil
	})
	return v.([]byte), t
}

func errorContent(p string, err error) []byte {
	prefix := "# "
	if strings.HasSuffix(p, ".sql") {
		prefix = "-- "
	}
	msg := strings.ReplaceAll(err.Error(), "\n", "\n"+prefix)
	return []byte(fmt.Sprintf("%sdbnfs: could not generate %s\n%s%s\n", prefix, p, prefix, msg))
}

// ---- billy.Filesystem ----

// Capabilities reports a read-only filesystem; go-nfs answers every
// mutating RPC with NFS3ERR_ROFS based on this.
func (f *FS) Capabilities() billy.Capability {
	return billy.ReadCapability | billy.SeekCapability
}

func (f *FS) Stat(p string) (os.FileInfo, error) {
	n, err := f.resolve("stat", p)
	if err != nil {
		return nil, err
	}
	return n.info, nil
}

func (f *FS) Lstat(p string) (os.FileInfo, error) { return f.Stat(p) }

func (f *FS) ReadDir(p string) ([]os.FileInfo, error) {
	n, err := f.resolve("readdir", p)
	if err != nil {
		return nil, err
	}
	if n.list == nil {
		return nil, &os.PathError{Op: "readdir", Path: p, Err: syscall.ENOTDIR}
	}
	return n.list()
}

func (f *FS) Open(p string) (billy.File, error) { return f.OpenFile(p, os.O_RDONLY, 0) }

func (f *FS) OpenFile(p string, flag int, _ os.FileMode) (billy.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		return nil, readOnly("open", p)
	}
	n, err := f.resolve("open", p)
	if err != nil {
		return nil, err
	}
	if n.list != nil {
		return nil, &os.PathError{Op: "open", Path: p, Err: syscall.EISDIR}
	}
	return &file{name: p, Reader: bytes.NewReader(n.content)}, nil
}

func (f *FS) Join(elem ...string) string { return path.Join(elem...) }

func (f *FS) Create(p string) (billy.File, error)        { return nil, readOnly("create", p) }
func (f *FS) Rename(from, _ string) error                { return readOnly("rename", from) }
func (f *FS) Remove(p string) error                      { return readOnly("remove", p) }
func (f *FS) TempFile(dir, _ string) (billy.File, error) { return nil, readOnly("tempfile", dir) }
func (f *FS) MkdirAll(p string, _ os.FileMode) error     { return readOnly("mkdir", p) }
func (f *FS) Symlink(_, link string) error               { return readOnly("symlink", link) }
func (f *FS) Readlink(p string) (string, error) {
	return "", &os.PathError{Op: "readlink", Path: p, Err: syscall.EINVAL}
}
func (f *FS) Chroot(p string) (billy.Filesystem, error) { return nil, billy.ErrNotSupported }
func (f *FS) Root() string                              { return "/" }

// file is an immutable in-memory file.
type file struct {
	name string
	*bytes.Reader
}

var _ billy.File = (*file)(nil)

func (f *file) Name() string              { return f.name }
func (f *file) Write([]byte) (int, error) { return 0, readOnly("write", f.name) }
func (f *file) Truncate(int64) error      { return readOnly("truncate", f.name) }
func (f *file) Close() error              { return nil }
func (f *file) Lock() error               { return nil }
func (f *file) Unlock() error             { return nil }
