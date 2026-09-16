// Command etcd-nfs serves an etcd v3 keyspace as an NFSv3 filesystem that
// the macOS and Linux kernel NFS clients can mount.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	nfs "github.com/willscott/go-nfs"
	"go.etcd.io/etcd/client/pkg/v3/transport"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/nontechno/experimental/etcd-nfs/etcdfs"
	"github.com/nontechno/experimental/etcd-nfs/nfsserver"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	var (
		listen          = flag.String("listen", "127.0.0.1:12049", "NFS and MOUNT listen address (null auth: keep it on loopback or a trusted network)")
		endpoints       = flag.String("endpoints", envOr("ETCDCTL_ENDPOINTS", "http://127.0.0.1:2379"), "comma-separated etcd endpoints (env ETCDCTL_ENDPOINTS)")
		cacert          = flag.String("cacert", os.Getenv("ETCDCTL_CACERT"), "etcd TLS CA bundle (env ETCDCTL_CACERT)")
		cert            = flag.String("cert", os.Getenv("ETCDCTL_CERT"), "etcd TLS client certificate (env ETCDCTL_CERT)")
		key             = flag.String("key", os.Getenv("ETCDCTL_KEY"), "etcd TLS client key (env ETCDCTL_KEY)")
		user            = flag.String("user", os.Getenv("ETCDCTL_USER"), "etcd user[:password] (env ETCDCTL_USER, ETCDCTL_PASSWORD)")
		prefix          = flag.String("prefix", "/", "etcd key prefix mapped to the filesystem root")
		readOnly        = flag.Bool("readonly", false, "serve read-only")
		uid             = flag.Uint("uid", uint(os.Getuid()), "owner uid reported for all entries")
		gid             = flag.Uint("gid", uint(os.Getgid()), "owner gid reported for all entries")
		fileMode        = flag.String("file-mode", "0644", "permission bits reported for files")
		dirMode         = flag.String("dir-mode", "0755", "permission bits reported for directories")
		maxFileSize     = flag.Int("max-file-size", 1<<20, "maximum file size in bytes")
		maxRequestBytes = flag.Int("max-request-bytes", 3<<19, "etcd server --max-request-bytes")
		maxTxnOps       = flag.Int("max-txn-ops", 128, "etcd server --max-txn-ops")
		quota           = flag.Int64("quota-bytes", 2<<30, "filesystem size reported when etcd does not report its quota")
		denyMacOS       = flag.Bool("deny-macos-metadata", false, "refuse creating .DS_Store, ._* and other Finder metadata")
		dialTimeout     = flag.Duration("dial-timeout", 5*time.Second, "etcd dial timeout")
		requestTimeout  = flag.Duration("request-timeout", 10*time.Second, "timeout per filesystem operation")
		logLevel        = flag.String("log-level", "info", "NFS log level: error, warn, info, debug, trace")
	)
	flag.Parse()

	fm, err := parseMode("file-mode", *fileMode)
	if err != nil {
		return err
	}
	dm, err := parseMode("dir-mode", *dirMode)
	if err != nil {
		return err
	}
	lvl, err := nfs.Log.ParseLevel(*logLevel)
	if err != nil {
		return fmt.Errorf("invalid -log-level %q", *logLevel)
	}
	nfs.Log.SetLevel(lvl)

	cfg := clientv3.Config{Endpoints: splitEndpoints(*endpoints), DialTimeout: *dialTimeout}
	if *cacert != "" || *cert != "" || *key != "" {
		tlsInfo := transport.TLSInfo{CertFile: *cert, KeyFile: *key, TrustedCAFile: *cacert}
		if cfg.TLS, err = tlsInfo.ClientConfig(); err != nil {
			return fmt.Errorf("etcd TLS: %w", err)
		}
	}
	if *user != "" {
		name, password, _ := strings.Cut(*user, ":")
		if password == "" {
			password = os.Getenv("ETCDCTL_PASSWORD")
		}
		cfg.Username, cfg.Password = name, password
	}
	cli, err := clientv3.New(cfg)
	if err != nil {
		return fmt.Errorf("etcd: %w", err)
	}
	defer cli.Close()

	opts := etcdfs.Options{
		Prefix:          *prefix,
		FileMode:        fm,
		DirMode:         dm,
		UID:             uint32(*uid),
		GID:             uint32(*gid),
		MaxFileSize:     *maxFileSize,
		MaxRequestBytes: *maxRequestBytes,
		MaxTxnOps:       *maxTxnOps,
		ReadOnly:        *readOnly,
		RequestTimeout:  *requestTimeout,
	}
	if *denyMacOS {
		opts.Deny = isMacOSMetadata
	}
	fsys, err := etcdfs.New(cli, opts)
	if err != nil {
		return fmt.Errorf("connecting to etcd at %s: %w", strings.Join(cfg.Endpoints, ","), err)
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	log.Printf("serving etcd prefix %q from %s on %s", fsys.Root(), strings.Join(cfg.Endpoints, ","), ln.Addr())
	if hint := mountHint(ln.Addr()); hint != "" {
		log.Printf("mount with: %s", hint)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		ln.Close()
	}()

	handler := nfsserver.New(fsys, nfsserver.Options{QuotaBytes: *quota})
	if err := nfs.Serve(ln, handler); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// splitEndpoints accepts etcdctl-style lists and tolerates spaces and
// trailing slashes ("http://10.0.0.1:2379/").
func splitEndpoints(s string) []string {
	var out []string
	for _, ep := range strings.Split(s, ",") {
		if ep = strings.TrimRight(strings.TrimSpace(ep), "/"); ep != "" {
			out = append(out, ep)
		}
	}
	return out
}

func parseMode(flagName, s string) (os.FileMode, error) {
	m, err := strconv.ParseUint(s, 8, 32)
	if err != nil || m > 0o777 {
		return 0, fmt.Errorf("invalid -%s %q: want octal permission bits such as 0644", flagName, s)
	}
	return os.FileMode(m), nil
}

// mountHint returns the mount command for this platform's NFS client.
func mountHint(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "SERVER"
	}
	switch runtime.GOOS {
	case "darwin":
		return fmt.Sprintf("sudo mount -t nfs -o vers=3,tcp,port=%s,mountport=%s,nolocks,noacl,soft %s:/ ~/etcd", port, port, host)
	case "linux":
		// addr/mountaddr let the kernel mount without the mount.nfs helper.
		return fmt.Sprintf("sudo mount -t nfs -o vers=3,proto=tcp,addr=%s,port=%s,mountaddr=%s,mountport=%s,mountproto=tcp,nolock,noacl,soft %s:/ /mnt/etcd",
			host, port, host, port, host)
	}
	return ""
}

func isMacOSMetadata(name string) bool {
	switch name {
	case ".DS_Store", ".Spotlight-V100", ".Trashes", ".fseventsd", ".TemporaryItems", ".VolumeIcon.icns":
		return true
	}
	return strings.HasPrefix(name, "._")
}
