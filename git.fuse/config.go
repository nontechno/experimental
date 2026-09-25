package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Duration is a time.Duration that decodes from strings like "2s" or "1m30s".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	if v < 0 {
		return fmt.Errorf("duration %q must not be negative", b)
	}
	d.Duration = v
	return nil
}

type Config struct {
	Repo struct {
		URL    string `toml:"url"`
		Branch string `toml:"branch"`
	} `toml:"repo"`

	Mount struct {
		Path         string   `toml:"path"`
		AllowOther   bool     `toml:"allow_other"`
		EntryTimeout Duration `toml:"entry_timeout"`
		AttrTimeout  Duration `toml:"attr_timeout"`
		Debug        bool     `toml:"debug"`
	} `toml:"mount"`

	State struct {
		Dir string `toml:"dir"`
	} `toml:"state"`

	Commit struct {
		AuthorName  string   `toml:"author_name"`
		AuthorEmail string   `toml:"author_email"`
		Message     string   `toml:"message"`
		QuietPeriod Duration `toml:"quiet_period"`
		MaxDelay    Duration `toml:"max_delay"`
	} `toml:"commit"`

	Sync struct {
		Push           bool     `toml:"push"`
		FetchInterval  Duration `toml:"fetch_interval"`
		NetworkTimeout Duration `toml:"network_timeout"`
	} `toml:"sync"`

	Auth struct {
		// "ssh", "token" or "none".
		Method string `toml:"method"`
		// ssh
		SSHKeyFile           string `toml:"ssh_key_file"`
		SSHKeyPassphraseFile string `toml:"ssh_key_passphrase_file"`
		KnownHostsFile       string `toml:"known_hosts_file"`
		// token (HTTPS basic auth)
		Username  string `toml:"username"`
		TokenFile string `toml:"token_file"`
		// Optional PEM bundle of extra CAs for HTTPS.
		CABundleFile string `toml:"ca_bundle_file"`
	} `toml:"auth"`

	Shutdown struct {
		UnmountTimeout Duration `toml:"unmount_timeout"`
	} `toml:"shutdown"`
}

func defaultConfig() *Config {
	c := &Config{}
	c.Mount.EntryTimeout.Duration = time.Second
	c.Mount.AttrTimeout.Duration = time.Second
	c.Commit.Message = "gitmount: automatic commit"
	c.Commit.QuietPeriod.Duration = 2 * time.Second
	c.Commit.MaxDelay.Duration = 30 * time.Second
	c.Sync.Push = true
	c.Sync.FetchInterval.Duration = 60 * time.Second
	c.Sync.NetworkTimeout.Duration = 2 * time.Minute
	c.Shutdown.UnmountTimeout.Duration = 10 * time.Second
	return c
}

// LoadConfig reads a TOML config file. Unknown keys are rejected so that a
// typo never silently falls back to a default.
func LoadConfig(path string) (*Config, error) {
	c := defaultConfig()
	md, err := toml.DecodeFile(path, c)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("%s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *Config) validate() error {
	var errs []error
	req := func(name, v string) {
		if strings.TrimSpace(v) == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	req("repo.url", c.Repo.URL)
	req("repo.branch", c.Repo.Branch)
	req("mount.path", c.Mount.Path)
	req("state.dir", c.State.Dir)
	req("commit.author_name", c.Commit.AuthorName)
	req("commit.author_email", c.Commit.AuthorEmail)
	req("commit.message", c.Commit.Message)
	req("auth.method", c.Auth.Method)

	for name, p := range map[string]string{"mount.path": c.Mount.Path, "state.dir": c.State.Dir} {
		if p != "" && !filepath.IsAbs(p) {
			errs = append(errs, fmt.Errorf("%s must be an absolute path", name))
		}
	}
	if c.Mount.Path != "" && c.State.Dir != "" {
		m, s := filepath.Clean(c.Mount.Path), filepath.Clean(c.State.Dir)
		if within(m, s) || within(s, m) {
			errs = append(errs, errors.New("mount.path and state.dir must not contain each other"))
		}
	}
	if c.Commit.QuietPeriod.Duration == 0 {
		errs = append(errs, errors.New("commit.quiet_period must be > 0"))
	}
	if c.Commit.MaxDelay.Duration < c.Commit.QuietPeriod.Duration {
		errs = append(errs, errors.New("commit.max_delay must be >= commit.quiet_period"))
	}
	if c.Sync.NetworkTimeout.Duration == 0 {
		errs = append(errs, errors.New("sync.network_timeout must be > 0"))
	}
	if err := validateAuth(c); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// within reports whether p is equal to or below dir.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// checkMountpoint verifies that the mountpoint is an existing, empty
// directory, so mounting never hides existing data.
func checkMountpoint(p string) error {
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("mountpoint %s does not exist", p)
		}
		return fmt.Errorf("mountpoint %s: %w (stale mount? try: fusermount3 -u %s)", p, err, p)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("mountpoint %s is not a directory", p)
	}
	names, err := f.Readdirnames(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) > 0 {
		return fmt.Errorf("mountpoint %s is not empty", p)
	}
	return nil
}
