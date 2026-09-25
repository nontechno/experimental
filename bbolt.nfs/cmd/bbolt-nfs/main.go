// Command bbolt-nfs serves a bbolt database over NFSv3: buckets are
// directories, keys are file names and values are file contents.
//
// The server speaks NFS with no authentication, so it listens on the loopback
// interface by default. Do not expose it to a network you do not control.
package main

import (
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	nfs "github.com/willscott/go-nfs"
	bolt "go.etcd.io/bbolt"

	"github.com/sport/bbolt-nfs/boltfs"
	"github.com/sport/bbolt-nfs/nfsserver"
)

// quietLogger downgrades one go-nfs message from error to debug.
//
// go-nfs answers NFSv3 EXCLUSIVE create with NFS3ERR_NOTSUPP and logs it as
// an error, but that status is part of normal negotiation: a client that
// wants O_EXCL asks for exclusive create first and retries with guarded
// create when the server says it cannot do it, so the file is still created
// with the right semantics. Logging it as an error makes a working operation
// look like a failure.
type quietLogger struct{ nfs.Logger }

func (l quietLogger) Errorf(format string, args ...interface{}) {
	if strings.Contains(format, "exclusive") {
		l.Logger.Debugf(format, args...)
		return
	}
	l.Logger.Errorf(format, args...)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("bbolt-nfs: ")

	dbPath := flag.String("db", "", "path to the bbolt database (required)")
	listen := flag.String("listen", "127.0.0.1:12049", "listen address for NFS and MOUNT")
	bucket := flag.String("bucket", "", "root the filesystem inside this top-level bucket (required for files at the top level)")
	readOnly := flag.Bool("readonly", false, "serve read-only and open the database read-only")
	uid := flag.Uint("uid", uint(os.Getuid()), "owner uid reported for every entry")
	gid := flag.Uint("gid", uint(os.Getgid()), "owner gid reported for every entry")
	fileMode := flag.String("file-mode", "0644", "permission bits reported for files")
	dirMode := flag.String("dir-mode", "0755", "permission bits reported for directories")
	maxFile := flag.Int64("max-file-size", 8<<20, "maximum size of one file (value)")
	maxRenameEntries := flag.Int("max-rename-entries", 100_000, "maximum entries moved by one directory rename")
	maxRenameBytes := flag.Int64("max-rename-bytes", 256<<20, "maximum bytes moved by one directory rename")
	lockTimeout := flag.Duration("lock-timeout", 5*time.Second, "how long to wait for the database file lock")
	denyMac := flag.Bool("deny-macos-metadata", false, "refuse .DS_Store, ._* and similar Finder files")
	logLevel := flag.String("log-level", "info", "log level: panic, fatal, error, warn, info, debug, trace")
	flag.Parse()

	if *dbPath == "" {
		log.Fatal("-db is required")
	}
	// bbolt initializes both a missing file and an existing zero-length one,
	// so the only case that needs a word of its own is a read-only export,
	// which has nothing to open.
	fresh := false
	if st, err := os.Stat(*dbPath); err != nil || st.Size() == 0 {
		if *readOnly {
			log.Fatalf("no database at %s: -readonly cannot create one", *dbPath)
		}
		fresh = true
	}
	fm, err1 := strconv.ParseUint(*fileMode, 8, 32)
	dm, err2 := strconv.ParseUint(*dirMode, 8, 32)
	if err1 != nil || err2 != nil {
		log.Fatal("invalid -file-mode or -dir-mode: expected octal, e.g. 0644")
	}

	// bbolt takes an exclusive lock on the file, so the timeout turns "another
	// process has it open" into a clear error instead of a hang.
	db, err := bolt.Open(*dbPath, 0o600, &bolt.Options{
		Timeout:  *lockTimeout,
		ReadOnly: *readOnly,
	})
	if err != nil {
		log.Fatalf("open %s: %v", *dbPath, err)
	}
	defer db.Close()

	opts := boltfs.Options{
		RootBucket:       *bucket,
		FileMode:         os.FileMode(fm),
		DirMode:          os.FileMode(dm),
		UID:              uint32(*uid),
		GID:              uint32(*gid),
		MaxFileSize:      *maxFile,
		MaxRenameEntries: *maxRenameEntries,
		MaxRenameBytes:   *maxRenameBytes,
		ReadOnly:         *readOnly,
	}
	if *denyMac {
		opts.Deny = func(name string) bool {
			switch name {
			case ".DS_Store", ".Spotlight-V100", ".Trashes", ".fseventsd", ".TemporaryItems", ".apdisk":
				return true
			}
			return strings.HasPrefix(name, "._")
		}
	}
	fsys, err := boltfs.New(db, opts)
	if err != nil {
		log.Fatal(err)
	}

	nfs.SetLogger(quietLogger{nfs.Log})
	if lvl, err := nfs.Log.ParseLevel(*logLevel); err == nil {
		nfs.Log.SetLevel(lvl)
	} else {
		log.Fatalf("invalid -log-level %q", *logLevel)
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	abs, _ := filepath.Abs(*dbPath)
	mode := "read-write"
	if *readOnly {
		mode = "read-only"
	}
	if fresh {
		log.Printf("initialized a new database at %s", abs)
	}
	log.Printf("serving %s (%s, root %s) on %s", abs, mode, fsys.Root(), ln.Addr())
	log.Printf("linux:  sudo mount -t nfs -o vers=3,tcp,port=%s,mountport=%s,nolock,soft 127.0.0.1:/ /mnt/bolt", port, port)
	log.Printf("macos:  sudo mount -t nfs -o vers=3,tcp,port=%s,mountport=%s,nolocks,soft,noacl 127.0.0.1:/ ~/bolt", port, port)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		log.Print("shutting down")
		_ = ln.Close()
	}()

	err = nfs.Serve(ln, nfsserver.New(fsys, nfsserver.Options{}))
	if err != nil && !errors.Is(err, net.ErrClosed) {
		log.Fatal(err)
	}
}
