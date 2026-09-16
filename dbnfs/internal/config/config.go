// Package config loads and validates the dbnfs configuration file.
//
// The file uses a strict subset of HOCON (the Typesafe/Lightbend config
// format used by many JVM services); see hocon.go for exactly what is
// accepted. Only the keys documented below are read; other keys are ignored.
//
//	database {
//	    url                = "jdbc:oracle:thin:@//localhost:1521/FREEPDB1"
//	    username           = "APP_RO"
//	    password           = "secret"           # or passwordEnv = "DBNFS_PASSWORD"
//	    authenticationType = "PASSWORD"
//	}
//
//	nfs {
//	    listen      = "127.0.0.1:12049"
//	    allowRemote = false
//	}
//
//	export {
//	    schemas      = ["HR", "SALES"]
//	    maxRows      = 100
//	    maxObjects   = 10000
//	    lobChars     = 1000
//	    maxFileBytes = 8388608
//	    cacheTTL     = "60s"
//	    queryTimeout = "30s"
//	}
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully validated configuration.
type Config struct {
	Database Database
	NFS      NFS
	Export   Export
}

// Database holds connection settings.
type Database struct {
	URL                string
	Username           string
	Password           string // never logged
	AuthenticationType string
	Target             Target // parsed from URL
	MaxConnections     int
}

// NFS holds listener settings.
type NFS struct {
	Listen      string
	AllowRemote bool
}

// Export controls what is exposed and how much work a single file may cost.
type Export struct {
	Schemas      []string // empty = all non Oracle-maintained schemas
	MaxRows      int      // rows written to rows.csv; 0 disables row export
	MaxObjects   int      // cap on entries in one directory listing
	LOBChars     int      // CLOB/NCLOB prefix length in rows.csv
	MaxFileBytes int      // hard cap on any generated file
	CacheTTL     time.Duration
	QueryTimeout time.Duration
}

// Defaults.
const (
	DefaultListen         = "127.0.0.1:12049"
	DefaultMaxRows        = 100
	DefaultMaxObjects     = 10000
	DefaultLOBChars       = 1000
	MaxLOBChars           = 1000 // DBMS_LOB.SUBSTR in SQL returns VARCHAR2 (<= 4000 bytes, 4 bytes/char worst case)
	DefaultMaxFileBytes   = 8 << 20
	DefaultCacheTTL       = 60 * time.Second
	DefaultQueryTimeout   = 30 * time.Second
	DefaultMaxConnections = 4
)

// LoadOptions tweak loading behaviour.
type LoadOptions struct {
	// InsecurePermissions allows a literal password in a file readable by
	// group or others.
	InsecurePermissions bool
}

// Load reads, parses and validates the file at path.
func Load(path string, opts LoadOptions) (*Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("config: %s is not a regular file", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg, hasLiteralPassword, err := Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if hasLiteralPassword && info.Mode().Perm()&0o077 != 0 && !opts.InsecurePermissions {
		return nil, fmt.Errorf("config %s: contains database.password but has mode %#o; "+
			"run `chmod 600 %s`, use database.passwordEnv instead, or pass -insecure-config-permissions",
			path, info.Mode().Perm(), path)
	}
	return cfg, nil
}

// Parse parses configuration text. It reports whether the password came
// from the file itself (as opposed to passwordEnv), so the caller can
// enforce file permissions.
func Parse(text string) (cfg *Config, hasLiteralPassword bool, err error) {
	h, err := parseHOCON(text)
	if err != nil {
		return nil, false, err
	}
	r := reader{h: h}

	cfg = &Config{}
	db := &cfg.Database
	db.URL = r.str("database.url", "")
	db.Username = r.str("database.username", "")
	db.AuthenticationType = strings.ToUpper(r.str("database.authenticationType", "PASSWORD"))
	db.MaxConnections = r.int("database.maxConnections", DefaultMaxConnections)

	passwordEnv := r.str("database.passwordEnv", "")
	if h.lookup("database.password") != nil {
		db.Password = r.str("database.password", "")
		hasLiteralPassword = true
	}

	cfg.NFS.Listen = r.str("nfs.listen", DefaultListen)
	cfg.NFS.AllowRemote = r.bool("nfs.allowRemote", false)

	ex := &cfg.Export
	ex.Schemas = r.strSlice("export.schemas")
	ex.MaxRows = r.int("export.maxRows", DefaultMaxRows)
	ex.MaxObjects = r.int("export.maxObjects", DefaultMaxObjects)
	ex.LOBChars = r.int("export.lobChars", DefaultLOBChars)
	ex.MaxFileBytes = r.int("export.maxFileBytes", DefaultMaxFileBytes)
	ex.CacheTTL = r.duration("export.cacheTTL", DefaultCacheTTL)
	ex.QueryTimeout = r.duration("export.queryTimeout", DefaultQueryTimeout)

	if r.err != nil {
		return nil, false, r.err
	}

	// Validation.
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	if db.URL == "" {
		add("database.url is required")
	} else if db.Target, err = ParseJDBCURL(db.URL); err != nil {
		add("database.url: %v", err)
	}
	if db.Username == "" {
		add("database.username is required")
	}
	if db.AuthenticationType != "PASSWORD" {
		add("database.authenticationType %q is not supported (only PASSWORD)", db.AuthenticationType)
	}
	switch {
	case hasLiteralPassword && passwordEnv != "":
		add("set only one of database.password and database.passwordEnv")
	case passwordEnv != "":
		v, ok := os.LookupEnv(passwordEnv)
		if !ok || v == "" {
			add("database.passwordEnv: environment variable %s is not set", passwordEnv)
		}
		db.Password = v
	case !hasLiteralPassword:
		add("database.password or database.passwordEnv is required")
	case db.Password == "":
		add("database.password is empty")
	}
	if db.MaxConnections < 1 || db.MaxConnections > 64 {
		add("database.maxConnections must be between 1 and 64")
	}

	if host, _, err := net.SplitHostPort(cfg.NFS.Listen); err != nil {
		add("nfs.listen: %v", err)
	} else if !cfg.NFS.AllowRemote && !isLoopback(host) {
		add("nfs.listen %q is not a loopback address; NFSv3 here has no authentication, "+
			"so set nfs.allowRemote = true only if the network is trusted", cfg.NFS.Listen)
	}

	for i, s := range ex.Schemas {
		s = strings.TrimSpace(s)
		if s == "" {
			add("export.schemas[%d] is empty", i)
		}
		ex.Schemas[i] = s
	}
	if len(ex.Schemas) > 1000 {
		add("export.schemas: at most 1000 entries")
	}
	if ex.MaxRows < 0 || ex.MaxRows > 1_000_000 {
		add("export.maxRows must be between 0 and 1000000")
	}
	if ex.MaxObjects < 1 || ex.MaxObjects > 1_000_000 {
		add("export.maxObjects must be between 1 and 1000000")
	}
	if ex.LOBChars < 0 || ex.LOBChars > MaxLOBChars {
		add("export.lobChars must be between 0 and %d", MaxLOBChars)
	}
	if ex.MaxFileBytes < 4096 || ex.MaxFileBytes > 1<<30 {
		add("export.maxFileBytes must be between 4096 and 1073741824")
	}
	if ex.CacheTTL < time.Second {
		add("export.cacheTTL must be at least 1s")
	}
	if ex.QueryTimeout < time.Second {
		add("export.queryTimeout must be at least 1s")
	}

	if len(problems) > 0 {
		return nil, false, errors.New(strings.Join(problems, "; "))
	}
	return cfg, hasLiteralPassword, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// reader performs typed lookups and records the first error.
type reader struct {
	h   hObject
	err error
}

func (r *reader) fail(path string, v any, msg string) {
	if r.err != nil {
		return
	}
	if sc, ok := v.(scalar); ok {
		r.err = fmt.Errorf("line %d: %s: %s", sc.line, path, msg)
		return
	}
	r.err = fmt.Errorf("%s: %s", path, msg)
}

func (r *reader) scalar(path string) (scalar, bool) {
	v := r.h.lookup(path)
	if v == nil {
		return scalar{}, false
	}
	sc, ok := v.(scalar)
	if !ok {
		r.fail(path, v, "expected a single value")
		return scalar{}, false
	}
	return sc, true
}

func (r *reader) str(path, def string) string {
	sc, ok := r.scalar(path)
	if !ok {
		return def
	}
	return sc.text
}

func (r *reader) int(path string, def int) int {
	sc, ok := r.scalar(path)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(sc.text)
	if err != nil {
		r.fail(path, sc, "expected an integer")
		return def
	}
	return n
}

func (r *reader) bool(path string, def bool) bool {
	sc, ok := r.scalar(path)
	if !ok {
		return def
	}
	switch strings.ToLower(sc.text) {
	case "true", "yes", "on":
		return true
	case "false", "no", "off":
		return false
	}
	r.fail(path, sc, "expected true or false")
	return def
}

func (r *reader) duration(path string, def time.Duration) time.Duration {
	sc, ok := r.scalar(path)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(sc.text)
	if err != nil {
		r.fail(path, sc, `expected a duration such as "30s" or "5m"`)
		return def
	}
	return d
}

func (r *reader) strSlice(path string) []string {
	v := r.h.lookup(path)
	if v == nil {
		return nil
	}
	arr, ok := v.(hArray)
	if !ok {
		r.fail(path, v, "expected an array of strings")
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, sc := range arr {
		out = append(out, sc.text)
	}
	return out
}
