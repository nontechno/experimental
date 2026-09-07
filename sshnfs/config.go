package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration that marshals to/from a human readable string
// ("30s", "1m") in JSON, and can be used directly as a flag.Value.
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

func (d *Duration) Set(s string) error {
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		*d = Duration(v)
		return nil
	}
	var n float64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("duration must be a string like \"30s\" or a number of seconds")
	}
	*d = Duration(time.Duration(n * float64(time.Second)))
	return nil
}

// stringList is a repeatable / comma separated string flag.
type stringList []string

func (s stringList) String() string { return strings.Join(s, ",") }

// stringListValue binds a stringList to a flag. The first Set of a given parse
// clears whatever the config file supplied, so the flag replaces the file
// rather than appending to it; later Sets in the same parse accumulate, which
// is what makes -i repeatable.
type stringListValue struct {
	list    *stringList
	cleared bool
}

func (v *stringListValue) String() string {
	if v == nil || v.list == nil {
		return ""
	}
	return v.list.String()
}

func (v *stringListValue) Set(val string) error {
	if v.list == nil {
		return fmt.Errorf("no destination for value %q", val)
	}
	if !v.cleared {
		*v.list = nil
		v.cleared = true
	}
	for _, part := range strings.Split(val, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*v.list = append(*v.list, part)
		}
	}
	return nil
}

// Config describes one export. A config file may also carry a "mounts" array;
// each entry is another Config that inherits every field not set in it from
// the enclosing one.
type Config struct {
	// ----- identity of this mount -----
	Name string `json:"name,omitempty"`

	// ----- remote -----
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	RemotePath string `json:"remote_path"` // empty => remote login directory

	// ----- authentication -----
	IdentityFiles      stringList `json:"identity_files"`
	IdentityPassphrase string     `json:"identity_passphrase"`
	Password           string     `json:"password"`
	AskPassword        bool       `json:"ask_password"`
	UseAgent           bool       `json:"use_agent"`
	KnownHostsFile     string     `json:"known_hosts_file"`
	InsecureHostKey    bool       `json:"insecure_ignore_host_key"`

	// ----- ~/.ssh/config -----
	UseSSHConfig  bool   `json:"use_ssh_config"`
	SSHConfigFile string `json:"ssh_config_file"`

	// ----- ssh / sftp tuning -----
	ConnectTimeout Duration `json:"connect_timeout"`
	KeepAlive      Duration `json:"keepalive_interval"`
	MaxPacket      int      `json:"max_packet"`
	MaxConcurrent  int      `json:"max_concurrent_requests"`

	// ----- reconnection -----
	Reconnect            bool     `json:"reconnect"`
	ReconnectDelay       Duration `json:"reconnect_delay"`
	ReconnectMaxDelay    Duration `json:"reconnect_max_delay"`
	ReconnectMaxAttempts int      `json:"reconnect_max_attempts"` // 0 = forever

	// ----- nfs server -----
	Listen       string   `json:"listen"`
	HandleCache  int      `json:"handle_cache_limit"`
	AttrCacheTTL Duration `json:"attr_cache_ttl"`
	DirCacheTTL  Duration `json:"dir_cache_ttl"`
	FileIdle     Duration `json:"open_file_idle"`
	FileCacheMax int      `json:"open_file_limit"`
	ReadOnly     bool     `json:"read_only"`
	UID          int      `json:"uid"` // -1 => report the remote uid as-is
	GID          int      `json:"gid"` // -1 => report the remote gid as-is

	// ----- local mount -----
	MountPoint    string `json:"mount_point"`
	MountOptions  string `json:"mount_options"`
	MountSudo     bool   `json:"mount_sudo"`
	UnmountOnExit bool   `json:"unmount_on_exit"`

	// ----- misc -----
	LogLevel string `json:"log_level"`

	// Mounts documents the schema; it is read as raw JSON so each entry can
	// inherit from its parent (see rawMounts) and is never re-emitted here.
	Mounts []*Config `json:"mounts,omitempty"`

	// not serialised
	alias         string          // the ~/.ssh/config alias, when Host was one
	explicit      map[string]bool // json keys the user set by hand
	rawMounts     []json.RawMessage
	configFile    string
	printConfig   bool
	exampleConfig bool
	showVersion   bool
}

const defaultMountOptionsDarwin = "vers=3,tcp,soft,intr,timeo=30,retrans=3,rsize=131072,wsize=131072,locallocks,noresvport,actimeo=1"

const defaultMountOptionsLinux = "vers=3,tcp,soft,timeo=30,retrans=3,rsize=131072,wsize=131072,nolock,noacl"

func defaultMountOptions() string {
	if runtime.GOOS == "darwin" {
		return defaultMountOptionsDarwin
	}
	return defaultMountOptionsLinux
}

// DefaultConfig returns the built-in defaults.
func DefaultConfig() *Config {
	uid, gid := -1, -1
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
		if n, err := strconv.Atoi(u.Uid); err == nil {
			uid = n
		}
		if n, err := strconv.Atoi(u.Gid); err == nil {
			gid = n
		}
	}
	return &Config{
		Port:                 22,
		User:                 name,
		UseAgent:             true,
		KnownHostsFile:       "~/.ssh/known_hosts",
		UseSSHConfig:         true,
		SSHConfigFile:        "~/.ssh/config",
		ConnectTimeout:       Duration(15 * time.Second),
		KeepAlive:            Duration(30 * time.Second),
		MaxPacket:            32768,
		MaxConcurrent:        64,
		Reconnect:            true,
		ReconnectDelay:       Duration(5 * time.Second),
		ReconnectMaxDelay:    Duration(time.Minute),
		ReconnectMaxAttempts: 0,
		Listen:               "127.0.0.1:0",
		HandleCache:          1000000,
		AttrCacheTTL:         Duration(time.Second),
		DirCacheTTL:          Duration(time.Second),
		FileIdle:             Duration(30 * time.Second),
		FileCacheMax:         64,
		UID:                  uid,
		GID:                  gid,
		MountOptions:         defaultMountOptions(),
		UnmountOnExit:        true,
		LogLevel:             "info",
		explicit:             map[string]bool{},
	}
}

// flagKeys maps a flag name to the config key it sets, for the settings that
// ~/.ssh/config could otherwise supply. A flag listed here always wins.
var flagKeys = map[string]string{
	"host":            "host",
	"port":            "port",
	"user":            "user",
	"i":               "identity_files",
	"known-hosts":     "known_hosts_file",
	"insecure":        "insecure_ignore_host_key",
	"connect-timeout": "connect_timeout",
	"keepalive":       "keepalive_interval",
	"F":               "ssh_config_file",
}

func (c *Config) markExplicit(key string) {
	if c.explicit == nil {
		c.explicit = map[string]bool{}
	}
	c.explicit[key] = true
}

func (c *Config) isExplicit(key string) bool { return c.explicit[key] }

func (c *Config) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.configFile, "config", c.configFile, "path to a JSON config file")
	fs.BoolVar(&c.exampleConfig, "example-config", false, "print an example config file and exit")
	fs.BoolVar(&c.printConfig, "print-config", false, "print the effective configuration and exit")
	fs.BoolVar(&c.showVersion, "version", false, "print version and exit")

	fs.StringVar(&c.Name, "name", c.Name, "label for this mount in log messages")
	fs.StringVar(&c.Host, "host", c.Host, "ssh host name, address, or ~/.ssh/config alias")
	fs.IntVar(&c.Port, "port", c.Port, "ssh server port")
	fs.StringVar(&c.User, "user", c.User, "ssh user name")
	fs.StringVar(&c.RemotePath, "remote", c.RemotePath, "remote directory to export (default: the login directory)")

	fs.Var(&stringListValue{list: &c.IdentityFiles}, "i", "ssh private key file; repeatable or comma separated")
	fs.StringVar(&c.IdentityPassphrase, "passphrase", c.IdentityPassphrase, "passphrase for the private key")
	fs.StringVar(&c.Password, "password", c.Password, "ssh password (prefer SSHNFS_PASSWORD or keys)")
	fs.BoolVar(&c.AskPassword, "ask-password", c.AskPassword, "prompt for the ssh password on the terminal")
	fs.BoolVar(&c.UseAgent, "agent", c.UseAgent, "try ssh-agent ($SSH_AUTH_SOCK) for authentication")
	fs.StringVar(&c.KnownHostsFile, "known-hosts", c.KnownHostsFile, "known_hosts file used to verify the host key")
	fs.BoolVar(&c.InsecureHostKey, "insecure", c.InsecureHostKey, "do not verify the ssh host key (dangerous)")

	fs.BoolVar(&c.UseSSHConfig, "use-ssh-config", c.UseSSHConfig, "read host settings from ~/.ssh/config")
	fs.StringVar(&c.SSHConfigFile, "F", c.SSHConfigFile, "ssh client config file to consult")

	fs.Var(&c.ConnectTimeout, "connect-timeout", "ssh connect, handshake and subsystem timeout")
	fs.Var(&c.KeepAlive, "keepalive", "ssh keepalive interval (0 disables)")
	fs.IntVar(&c.MaxPacket, "max-packet", c.MaxPacket, "maximum sftp packet size in bytes")
	fs.IntVar(&c.MaxConcurrent, "max-concurrent", c.MaxConcurrent, "maximum concurrent sftp requests")

	fs.BoolVar(&c.Reconnect, "reconnect", c.Reconnect, "reconnect in the background after a dropped connection")
	fs.Var(&c.ReconnectDelay, "reconnect-delay", "pause before the first reconnection attempt")
	fs.Var(&c.ReconnectMaxDelay, "reconnect-max-delay", "ceiling for the reconnection backoff")
	fs.IntVar(&c.ReconnectMaxAttempts, "reconnect-attempts", c.ReconnectMaxAttempts, "give up after this many attempts (0 = never)")

	fs.StringVar(&c.Listen, "listen", c.Listen, "address the NFS server listens on")
	fs.IntVar(&c.HandleCache, "handle-cache", c.HandleCache, "maximum number of cached NFS file handles")
	fs.Var(&c.AttrCacheTTL, "attr-cache", "how long file attributes are cached (0 disables)")
	fs.Var(&c.DirCacheTTL, "dir-cache", "how long directory listings are cached (0 disables)")
	fs.Var(&c.FileIdle, "open-file-idle", "how long an idle sftp file handle is kept open")
	fs.IntVar(&c.FileCacheMax, "open-file-limit", c.FileCacheMax, "maximum number of sftp file handles kept open")
	fs.BoolVar(&c.ReadOnly, "read-only", c.ReadOnly, "export the remote filesystem read-only")
	fs.IntVar(&c.UID, "uid", c.UID, "uid reported for every file (-1 passes the remote uid through)")
	fs.IntVar(&c.GID, "gid", c.GID, "gid reported for every file (-1 passes the remote gid through)")

	fs.StringVar(&c.MountPoint, "mount", c.MountPoint, "local directory to mount the export on")
	fs.StringVar(&c.MountOptions, "mount-options", c.MountOptions, "options passed to mount -o")
	fs.BoolVar(&c.MountSudo, "mount-sudo", c.MountSudo, "run mount/umount through sudo")
	fs.BoolVar(&c.UnmountOnExit, "unmount-on-exit", c.UnmountOnExit, "unmount the mount point when the server stops")

	fs.StringVar(&c.LogLevel, "log-level", c.LogLevel, "panic, fatal, error, warn, info, debug or trace")
}

func usage(fs *flag.FlagSet, out io.Writer) func() {
	return func() {
		fmt.Fprintf(out, `sshnfs - mount remote directories over SSH/SFTP by serving them as NFSv3.

Usage:
  sshnfs [flags] [user@host[:/remote/path]] [mountpoint]

Examples:
  sshnfs -mount ~/mnt/build build@buildhost:/srv/work
  sshnfs buildhost:/srv/work ~/mnt/build     # buildhost from ~/.ssh/config
  sshnfs -config ~/.config/sshnfs.json       # one mount, or many

Environment:
  SSHNFS_PASSWORD, SSHNFS_PASSPHRASE  used when not set by flag or config file

Flags:
`)
		fs.PrintDefaults()
	}
}

// parseArgs parses flags that may be interspersed with positional arguments;
// Go's flag package stops at the first non-flag on its own.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		rest = fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
}

const redacted = "***"

// MarshalJSON keeps secrets out of -print-config output and out of any config
// file written from it. Redacted values are not valid input; replace them
// before feeding such a file back in.
func (c *Config) MarshalJSON() ([]byte, error) {
	type shadow Config
	v := shadow(*c)
	if v.Password != "" {
		v.Password = redacted
	}
	if v.IdentityPassphrase != "" {
		v.IdentityPassphrase = redacted
	}
	return json.Marshal(v)
}

// LoadConfig resolves defaults, then the config file, then the command line,
// then ~/.ssh/config for whatever is still unset. It returns one Config per
// mount; without a "mounts" array that is a single entry.
func LoadConfig(args []string, out io.Writer) ([]*Config, error) {
	// Pass one: we only care about -config (and about reporting usage errors).
	probe := DefaultConfig()
	fs1 := flag.NewFlagSet("sshnfs", flag.ContinueOnError)
	fs1.SetOutput(out)
	probe.bind(fs1)
	fs1.Usage = usage(fs1, out)
	if _, err := parseArgs(fs1, args); err != nil {
		return nil, err
	}

	cfg := DefaultConfig()
	if probe.exampleConfig {
		cfg.exampleConfig = true
		return []*Config{cfg}, nil
	}
	if probe.configFile != "" {
		if err := cfg.loadFile(probe.configFile); err != nil {
			return nil, err
		}
	}

	// Pass two: bind again, this time the flag defaults are the config file
	// values, so only flags actually present on the command line override.
	fs2 := flag.NewFlagSet("sshnfs", flag.ContinueOnError)
	fs2.SetOutput(out)
	cfg.bind(fs2)
	fs2.Usage = usage(fs2, out)
	positional, err := parseArgs(fs2, args)
	if err != nil {
		return nil, err
	}
	fs2.Visit(func(f *flag.Flag) {
		if key, ok := flagKeys[f.Name]; ok {
			cfg.markExplicit(key)
		}
	})

	if len(positional) > 0 && len(cfg.rawMounts) > 0 {
		return nil, fmt.Errorf("a config file with a \"mounts\" array cannot be combined with a host on the command line")
	}
	if err := cfg.applyPositional(positional); err != nil {
		return nil, err
	}

	mounts, err := cfg.deriveMounts()
	if err != nil {
		return nil, err
	}
	seenMount := map[string]string{}
	seenListen := map[string]string{}
	for i, m := range mounts {
		m.applyEnv()
		if err := m.applySSHConfig(); err != nil {
			return nil, fmt.Errorf("mount %s: %w", m.label(i), err)
		}
		if err := m.validate(); err != nil {
			if len(mounts) > 1 {
				return nil, fmt.Errorf("mount %s: %w", m.label(i), err)
			}
			return nil, err
		}
		if m.MountPoint != "" {
			if prev, dup := seenMount[m.MountPoint]; dup {
				return nil, fmt.Errorf("mount point %s is claimed by both %s and %s", m.MountPoint, prev, m.label(i))
			}
			seenMount[m.MountPoint] = m.label(i)
		}
		if isFixedPort(m.Listen) {
			if prev, dup := seenListen[m.Listen]; dup {
				return nil, fmt.Errorf("listen address %s is claimed by both %s and %s", m.Listen, prev, m.label(i))
			}
			seenListen[m.Listen] = m.label(i)
		}
	}
	return mounts, nil
}

// isFixedPort reports whether a listen address names a specific port, so
// duplicates can be rejected; port 0 means "pick one" and never collides.
func isFixedPort(addr string) bool {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return false
	}
	port := addr[i+1:]
	return port != "" && port != "0"
}

func (c *Config) label(i int) string {
	switch {
	case c.Name != "":
		return c.Name
	case c.alias != "":
		return c.alias
	case c.Host != "" && c.RemotePath != "":
		return c.Host + ":" + c.RemotePath
	case c.Host != "":
		return c.Host
	default:
		return fmt.Sprintf("#%d", i+1)
	}
}

// Label names this mount in logs and errors.
func (c *Config) Label() string { return c.label(0) }

func (c *Config) loadFile(path string) error {
	data, err := os.ReadFile(expandUser(path))
	if err != nil {
		return fmt.Errorf("config file: %w", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return fmt.Errorf("config file %s: %w", path, err)
	}
	if raw, ok := keys["mounts"]; ok {
		if err := json.Unmarshal(raw, &c.rawMounts); err != nil {
			return fmt.Errorf("config file %s: \"mounts\": %w", path, err)
		}
		if len(c.rawMounts) == 0 {
			return fmt.Errorf("config file %s: \"mounts\" is empty", path)
		}
		delete(keys, "mounts")
	}
	if err := c.decodeKeys(keys, "config file "+path); err != nil {
		return err
	}
	c.configFile = path
	return nil
}

// decodeKeys applies a set of JSON keys to c and records which ones the user
// actually wrote, so applySSHConfig knows what it may fill in.
func (c *Config) decodeKeys(keys map[string]json.RawMessage, where string) error {
	body, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(c); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	for k := range keys {
		c.markExplicit(k)
	}
	return nil
}

func (c *Config) clone() *Config {
	cp := *c
	cp.IdentityFiles = append(stringList(nil), c.IdentityFiles...)
	cp.explicit = make(map[string]bool, len(c.explicit))
	for k, v := range c.explicit {
		cp.explicit[k] = v
	}
	cp.rawMounts = nil
	cp.Mounts = nil
	return &cp
}

// deriveMounts expands the "mounts" array. Each entry starts as a copy of the
// enclosing configuration, so shared settings are written once.
func (c *Config) deriveMounts() ([]*Config, error) {
	if len(c.rawMounts) == 0 {
		return []*Config{c}, nil
	}
	out := make([]*Config, 0, len(c.rawMounts))
	for i, raw := range c.rawMounts {
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			return nil, fmt.Errorf("mounts[%d]: %w", i, err)
		}
		if _, nested := keys["mounts"]; nested {
			return nil, fmt.Errorf("mounts[%d]: nested \"mounts\" is not supported", i)
		}
		m := c.clone()
		if err := m.decodeKeys(keys, fmt.Sprintf("mounts[%d]", i)); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (c *Config) applyEnv() {
	if c.Password == "" {
		// Safer than -password, which is visible in ps.
		c.Password = os.Getenv("SSHNFS_PASSWORD")
	}
	if c.IdentityPassphrase == "" {
		c.IdentityPassphrase = os.Getenv("SSHNFS_PASSPHRASE")
	}
}

// applyPositional understands the familiar "[user@]host[:/path] [mountpoint]"
// form so the tool can be used the way sshfs is.
func (c *Config) applyPositional(args []string) error {
	if len(args) > 2 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(args[2:], " "))
	}
	if len(args) >= 1 && args[0] != "" {
		target := args[0]
		if i := strings.Index(target, "@"); i >= 0 {
			c.User = target[:i]
			c.markExplicit("user")
			target = target[i+1:]
		}
		if i := strings.Index(target, ":"); i >= 0 {
			c.RemotePath = target[i+1:]
			target = target[:i]
		}
		if target != "" {
			c.Host = target
			c.markExplicit("host")
		}
	}
	if len(args) == 2 && args[1] != "" {
		c.MountPoint = args[1]
	}
	return nil
}

func (c *Config) validate() error {
	if c.Password == redacted || c.IdentityPassphrase == redacted {
		return fmt.Errorf("configuration contains a redacted secret (%s); replace it with the real value or remove the field", redacted)
	}
	if c.Host == "" {
		return fmt.Errorf("no ssh host given (use -host, a config file, or user@host:/path)")
	}
	if c.User == "" {
		return fmt.Errorf("no ssh user given (use -user or user@host)")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("invalid ssh port %d", c.Port)
	}
	if c.MaxPacket < 1024 {
		return fmt.Errorf("max_packet must be at least 1024")
	}
	if c.HandleCache < 2 {
		return fmt.Errorf("handle_cache_limit must be at least 2")
	}
	if c.FileCacheMax < 1 {
		return fmt.Errorf("open_file_limit must be at least 1")
	}
	if c.Listen == "" {
		return fmt.Errorf("listen address must not be empty")
	}
	if !c.InsecureHostKey && c.KnownHostsFile == "" {
		return fmt.Errorf("either -known-hosts or -insecure is required")
	}
	if c.ReconnectDelay < 0 || c.ReconnectMaxDelay < 0 {
		return fmt.Errorf("reconnect delays must not be negative")
	}
	if c.ReconnectMaxDelay > 0 && c.ReconnectMaxDelay < c.ReconnectDelay {
		return fmt.Errorf("reconnect_max_delay must not be smaller than reconnect_delay")
	}
	if _, err := parseLogLevel(c.LogLevel); err != nil {
		return err
	}
	if c.MountPoint != "" {
		c.MountPoint = expandUser(c.MountPoint)
		abs, err := filepath.Abs(c.MountPoint)
		if err != nil {
			return fmt.Errorf("mount point: %w", err)
		}
		c.MountPoint = abs
		fi, err := os.Stat(c.MountPoint)
		if err != nil {
			return fmt.Errorf("mount point %s: %w", c.MountPoint, err)
		}
		if !fi.IsDir() {
			return fmt.Errorf("mount point %s is not a directory", c.MountPoint)
		}
	}
	return nil
}

func (c *Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

func (c *Config) JSON() string {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

func expandUser(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}
