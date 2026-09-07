package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"
)

const version = "1.1.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	mounts, err := LoadConfig(args, os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "sshnfs: %v\n", err)
		return 2
	}
	first := mounts[0]
	switch {
	case first.showVersion:
		fmt.Printf("sshnfs %s\n", version)
		return 0
	case first.exampleConfig:
		fmt.Println(DefaultConfig().JSON())
		return 0
	case first.printConfig:
		if len(mounts) == 1 {
			fmt.Println(first.JSON())
			return 0
		}
		out, err := json.MarshalIndent(mounts, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "sshnfs: %v\n", err)
			return 1
		}
		fmt.Println(string(out))
		return 0
	}

	level, _ := parseLogLevel(first.LogLevel)
	log := NewLogger(level)
	nfs.Log.SetLevel(level.nfsLevel())

	// Bring every mount up before mounting any of them, so a failure on the
	// third host does not leave the first two mounted.
	servers := make([]*server, 0, len(mounts))
	shutdown := func() {
		for i := len(servers) - 1; i >= 0; i-- {
			servers[i].close()
		}
	}
	for _, cfg := range mounts {
		srv, err := newServer(cfg, log, len(mounts) > 1)
		if err != nil {
			log.Errorf("%s: %v", cfg.Label(), err)
			shutdown()
			return 1
		}
		servers = append(servers, srv)
	}

	serveErr := make(chan error, len(servers))
	for _, srv := range servers {
		srv.serve(serveErr)
	}

	for _, srv := range servers {
		if srv.cfg.MountPoint == "" {
			srv.log.Infof("not mounted; mount it with:\n  %s",
				strings.Join(MountCommand(srv.cfg, srv.host, srv.port, "/path/to/mountpoint"), " "))
			continue
		}
		if err := srv.mount(); err != nil {
			srv.log.Errorf("mount failed: %v", err)
			shutdown()
			return 1
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	exit := 0
	select {
	case s := <-sig:
		log.Infof("got %s, shutting down", s)
	case err := <-serveErr:
		if err != nil {
			log.Errorf("nfs server stopped: %v", err)
			exit = 1
		}
	}

	shutdown()
	return exit
}

// server is one export: an SSH connection, the filesystem on top of it, the
// NFS listener, and the local mount if there is one.
type server struct {
	cfg  *Config
	log  *Logger
	conn *Conn
	fs   *FS

	listener net.Listener
	host     string
	port     int

	mu      sync.Mutex
	mounted bool
	closed  bool
}

func newServer(cfg *Config, log *Logger, labelled bool) (*server, error) {
	if labelled {
		log = log.With(cfg.Label())
	}
	conn := NewConn(cfg, log)

	root, err := resolveRoot(conn, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	where := cfg.Addr()
	if cfg.alias != "" {
		where = fmt.Sprintf("%s (%s)", cfg.alias, where)
	}
	log.Infof("connected to %s@%s, exporting %s", cfg.User, where, root)

	fsys := NewFS(cfg, conn, log, root)

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		fsys.Close()
		conn.Close()
		return nil, fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		listener.Close()
		fsys.Close()
		conn.Close()
		return nil, fmt.Errorf("unexpected listener address %s", listener.Addr())
	}
	host := addr.IP.String()
	if addr.IP.IsUnspecified() {
		host = "127.0.0.1"
	}

	mode := "read/write"
	if cfg.ReadOnly {
		mode = "read-only"
	}
	log.Infof("serving NFSv3 (%s) on %s", mode, listener.Addr())

	return &server{
		cfg:      cfg,
		log:      log,
		conn:     conn,
		fs:       fsys,
		listener: listener,
		host:     host,
		port:     addr.Port,
	}, nil
}

func (s *server) serve(errCh chan<- error) {
	handler := &statHandler{
		Handler: nfshelper.NewCachingHandler(nfshelper.NewNullAuthHandler(s.fs), s.cfg.HandleCache),
		fs:      s.fs,
		cfg:     s.cfg,
	}
	go func() {
		err := nfs.Serve(s.listener, handler)
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return // expected during shutdown
		}
		errCh <- err
	}()
}

func (s *server) mount() error {
	if err := Mount(context.Background(), s.cfg, s.host, s.port); err != nil {
		return err
	}
	s.mu.Lock()
	s.mounted = true
	s.mu.Unlock()
	s.log.Infof("mounted on %s", s.cfg.MountPoint)
	return nil
}

func (s *server) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	mounted := s.mounted
	s.mu.Unlock()

	// Unmount first, while the server is still answering, so the client is
	// not left with in-flight requests to a dead port.
	if mounted && s.cfg.UnmountOnExit {
		if err := Unmount(s.cfg); err != nil {
			s.log.Errorf("unmount: %v", err)
		} else {
			s.log.Infof("unmounted %s", s.cfg.MountPoint)
		}
	}
	s.listener.Close()
	s.fs.Close()
	s.conn.Close()
}

// resolveRoot connects eagerly so configuration problems surface immediately,
// and turns an empty or ~-relative remote path into an absolute one.
func resolveRoot(conn *Conn, cfg *Config) (string, error) {
	client, _, err := conn.Client()
	if err != nil {
		return "", err
	}
	root := cfg.RemotePath
	if root == "" || root == "~" || strings.HasPrefix(root, "~/") {
		home, err := client.Getwd()
		if err != nil {
			return "", fmt.Errorf("determine remote home directory: %w", err)
		}
		if strings.HasPrefix(root, "~/") {
			root = path.Join(home, root[2:])
		} else {
			root = home
		}
	}
	if !path.IsAbs(root) {
		wd, err := client.Getwd()
		if err != nil {
			return "", fmt.Errorf("determine remote working directory: %w", err)
		}
		root = path.Join(wd, root)
	}
	fi, err := client.Stat(root)
	if err != nil {
		return "", fmt.Errorf("remote path %s: %w", root, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("remote path %s is not a directory", root)
	}
	return normalizeRoot(root), nil
}

// statHandler adds real free-space reporting on top of the caching handler.
type statHandler struct {
	nfs.Handler
	fs  *FS
	cfg *Config
}

func (h *statHandler) FSStat(ctx context.Context, f billy.Filesystem, s *nfs.FSStat) error {
	// Sensible fallback for servers without the OpenSSH statvfs extension.
	const fallback = uint64(1) << 40
	s.TotalSize, s.FreeSize, s.AvailableSize = fallback, fallback, fallback
	s.TotalFiles, s.FreeFiles, s.AvailableFiles = 1<<20, 1<<20, 1<<20
	s.CacheHint = h.cfg.AttrCacheTTL.D()
	if s.CacheHint <= 0 {
		s.CacheHint = time.Second
	}

	st, err := h.fs.StatVFS()
	if err != nil {
		return nil
	}
	bsize := st.Frsize
	if bsize == 0 {
		bsize = st.Bsize
	}
	if bsize == 0 {
		bsize = 4096
	}
	if h.cfg.ReadOnly {
		s.TotalSize = st.Blocks * bsize
		s.FreeSize, s.AvailableSize = 0, 0
	} else {
		s.TotalSize = st.Blocks * bsize
		s.FreeSize = st.Bfree * bsize
		s.AvailableSize = st.Bavail * bsize
	}
	s.TotalFiles = st.Files
	s.FreeFiles = st.Ffree
	s.AvailableFiles = st.Favail
	return nil
}
