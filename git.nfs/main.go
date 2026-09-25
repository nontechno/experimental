package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	nfs "github.com/willscott/go-nfs"
	"github.com/willscott/go-nfs/helpers"
)

func main() {
	configPath := flag.String("config", "gitnfs.yaml", "path to YAML configuration")
	flag.Parse()
	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	repo, err := openGit(cfg)
	if err != nil {
		log.Fatal(err)
	}
	fs := &exportFS{Filesystem: osfs.New(cfg.WorkDirectory, osfs.WithBoundOS()), repo: repo}
	base := helpers.NewNullAuthHandler(fs)
	allowed := make([]*net.IPNet, 0, len(cfg.AllowedClients))
	for _, cidr := range cfg.AllowedClients {
		_, network, _ := net.ParseCIDR(cidr)
		allowed = append(allowed, network)
	}
	handler := helpers.NewCachingHandler(&accessHandler{Handler: base, fs: fs, allowed: allowed}, 10000)
	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		log.Fatal(fmt.Errorf("listen: %w", err))
	}
	if len(allowed) > 0 {
		listener = &filterListener{Listener: listener, allowed: allowed}
	}
	log.Printf("serving %s branch %s over NFSv3 on %s", cfg.Repository, cfg.Branch, listener.Addr())
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-stop; _ = listener.Close() }()
	if err := nfs.Serve(listener, handler); err != nil && !errors.Is(err, net.ErrClosed) {
		log.Fatal(err)
	}
}

type accessHandler struct {
	nfs.Handler
	fs      billy.Filesystem
	allowed []*net.IPNet
}

func (h *accessHandler) Mount(ctx context.Context, conn net.Conn, req nfs.MountRequest) (nfs.MountStatus, billy.Filesystem, []nfs.AuthFlavor) {
	if string(req.Dirpath) != "/" {
		return nfs.MountStatusErrNoEnt, nil, nil
	}
	if len(h.allowed) > 0 {
		host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
		ip := net.ParseIP(host)
		ok := false
		if err == nil {
			for _, n := range h.allowed {
				if n.Contains(ip) {
					ok = true
					break
				}
			}
		}
		if !ok {
			return nfs.MountStatusErrAcces, nil, nil
		}
	}
	return nfs.MountStatusOk, h.fs, []nfs.AuthFlavor{nfs.AuthFlavorNull}
}

type filterListener struct {
	net.Listener
	allowed []*net.IPNet
}

func (l *filterListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		host, _, splitErr := net.SplitHostPort(conn.RemoteAddr().String())
		ip := net.ParseIP(host)
		permitted := false
		if splitErr == nil {
			for _, network := range l.allowed {
				if network.Contains(ip) {
					permitted = true
					break
				}
			}
		}
		if permitted {
			return conn, nil
		}
		_ = conn.Close()
	}
}
