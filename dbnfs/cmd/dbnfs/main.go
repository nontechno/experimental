// Command dbnfs serves read-only Oracle catalog information over NFSv3.
//
//	dbnfs -config dbnfs.conf
//
// See README.md for configuration and mount instructions.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	nfs "github.com/willscott/go-nfs"

	"dbnfs/internal/config"
	"dbnfs/internal/dbfs"
	"dbnfs/internal/oracle"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dbnfs:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath = flag.String("config", "", "path to the configuration file (required)")
		check      = flag.Bool("check", false, "connect, list visible schemas and object counts, then exit")
		insecure   = flag.Bool("insecure-config-permissions", false, "allow database.password in a group/world-readable config file")
		debug      = flag.Bool("debug", false, "verbose logging, including NFS requests")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: dbnfs -config FILE [-check] [-debug]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *configPath == "" || flag.NArg() > 0 {
		flag.Usage()
		return errors.New("-config is required")
	}

	level := slog.LevelInfo
	nfs.Log.SetLevel(nfs.PanicLevel) // go-nfs logs every ENOENT/EROFS as an error
	if *debug {
		level = slog.LevelDebug
		nfs.Log.SetLevel(nfs.DebugLevel)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	cfg, err := config.Load(*configPath, config.LoadOptions{InsecurePermissions: *insecure})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("connecting", "target", cfg.Database.Target.String(), "user", cfg.Database.Username)
	db, err := oracle.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	src := oracle.New(db, cfg.Export, log)

	if *check {
		return runCheck(ctx, src, cfg.Export.QueryTimeout)
	}

	// Fail fast on privilege or filter problems instead of at first mount.
	sctx, cancel := context.WithTimeout(ctx, cfg.Export.QueryTimeout)
	schemas, err := src.Schemas(sctx)
	cancel()
	if err != nil {
		return fmt.Errorf("listing schemas: %w", err)
	}
	if len(schemas) == 0 {
		log.Warn("no schemas visible; check export.schemas and the account's privileges")
	} else {
		log.Info("schemas visible", "count", len(schemas))
	}

	fsys := dbfs.New(src, dbfs.Options{
		CacheTTL:     cfg.Export.CacheTTL,
		QueryTimeout: cfg.Export.QueryTimeout,
		MaxFileBytes: cfg.Export.MaxFileBytes,
		Logger:       log,
	})

	listener, err := net.Listen("tcp", cfg.NFS.Listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.NFS.Listen, err)
	}
	addr := listener.Addr().(*net.TCPAddr)
	if !addr.IP.IsLoopback() {
		log.Warn("serving on a non-loopback address without authentication; anyone who can reach this port can read the export",
			"listen", addr.String())
	}
	log.Info("serving NFSv3 (read-only)", "listen", addr.String())
	printMountHelp(addr)

	errc := make(chan error, 1)
	go func() { errc <- nfs.Serve(listener, dbfs.NewHandler(fsys)) }()

	select {
	case <-ctx.Done():
		log.Info("shutting down; unmount clients first to avoid hung processes")
		listener.Close()
		select {
		case <-errc:
		case <-time.After(2 * time.Second):
		}
		return nil
	case err := <-errc:
		return fmt.Errorf("nfs server: %w", err)
	}
}

func runCheck(ctx context.Context, src *oracle.Source, timeout time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	schemas, err := src.Schemas(cctx)
	if err != nil {
		return fmt.Errorf("listing schemas: %w", err)
	}
	fmt.Printf("schemas (%d):", len(schemas))
	for _, s := range schemas {
		fmt.Printf(" %s", s)
	}
	fmt.Println()
	for _, k := range src.Kinds() {
		kctx, kcancel := context.WithTimeout(ctx, timeout)
		objs, err := src.Objects(kctx, k.Dir)
		kcancel()
		if err != nil {
			return fmt.Errorf("listing %s: %w", k.Dir, err)
		}
		fmt.Printf("%-10s %d\n", k.Dir, len(objs))
	}
	fmt.Println("ok")
	return nil
}

func printMountHelp(addr *net.TCPAddr) {
	host := addr.IP.String()
	if addr.IP.IsUnspecified() {
		host = "<server-address>"
	}
	port := strconv.Itoa(addr.Port)
	switch runtime.GOOS {
	case "darwin":
		fmt.Fprintf(os.Stderr, "\nmount (macOS):\n  mkdir -p ~/oradb && sudo mount -t nfs -o vers=3,tcp,port=%s,mountport=%s,nolocks,rdonly,soft,nobrowse %s:/ ~/oradb\n\n",
			port, port, host)
	default:
		fmt.Fprintf(os.Stderr, "\nmount (Linux):\n  sudo mkdir -p /mnt/oradb && sudo mount -t nfs -o vers=3,proto=tcp,port=%s,mountport=%s,mountproto=tcp,nolock,noacl,ro,soft,timeo=100,retrans=2 %s:/ /mnt/oradb\n\n",
			port, port, host)
	}
}
