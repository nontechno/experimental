package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kevinburke/ssh_config"
)

// systemSSHConfig is consulted after the user's file, as ssh does.
const systemSSHConfig = "/etc/ssh/ssh_config"

// sshConfigSet is an ordered list of parsed ssh client config files. The first
// file to define a keyword wins, which is how ssh resolves options.
type sshConfigSet struct {
	files []*ssh_config.Config
}

func (s *sshConfigSet) get(alias, key string) string {
	if s == nil {
		return ""
	}
	for _, f := range s.files {
		if v, err := f.Get(alias, key); err == nil && v != "" {
			return v
		}
	}
	return ""
}

func (s *sshConfigSet) getAll(alias, key string) []string {
	if s == nil {
		return nil
	}
	var out []string
	for _, f := range s.files {
		if vs, err := f.GetAll(alias, key); err == nil {
			out = append(out, vs...)
		}
	}
	return out
}

func decodeSSHConfig(path string) (*ssh_config.Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ssh_config.Decode(f)
}

// loadSSHConfig reads the user's file (missing is fine unless they named it
// explicitly) followed by the system one.
func loadSSHConfig(path string, required bool) (*sshConfigSet, error) {
	set := &sshConfigSet{}
	if path != "" {
		expanded := expandUser(path)
		cfg, err := decodeSSHConfig(expanded)
		switch {
		case err == nil:
			set.files = append(set.files, cfg)
		case os.IsNotExist(err) && !required:
			// A missing ~/.ssh/config is entirely normal.
		default:
			return nil, fmt.Errorf("ssh config %s: %w", expanded, err)
		}
	}
	if cfg, err := decodeSSHConfig(systemSSHConfig); err == nil {
		set.files = append(set.files, cfg)
	}
	if len(set.files) == 0 {
		return nil, nil
	}
	return set, nil
}

// expandSSHTokens substitutes the percent escapes ssh understands in the
// values we consume. Unknown escapes are left alone rather than mangled.
func expandSSHTokens(s string, alias, host, user string, port int) string {
	if !strings.ContainsRune(s, '%') {
		return s
	}
	home, _ := os.UserHomeDir()
	r := strings.NewReplacer(
		"%%", "%",
		"%h", host,
		"%n", alias,
		"%r", user,
		"%p", strconv.Itoa(port),
		"%d", home,
		"%L", shortLocalHostname(),
		"%l", localHostname(),
	)
	return r.Replace(s)
}

func localHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

func shortLocalHostname() string {
	h := localHostname()
	if i := strings.Index(h, "."); i > 0 {
		return h[:i]
	}
	return h
}

// applySSHConfig fills in whatever the user did not state explicitly from
// ~/.ssh/config, treating Host as an alias to look up. Honoured keywords:
// HostName, Port, User, IdentityFile, UserKnownHostsFile,
// StrictHostKeyChecking, ConnectTimeout and ServerAliveInterval. Anything else
// in the file (ProxyJump, ProxyCommand, ControlMaster, …) is ignored.
func (c *Config) applySSHConfig() error {
	if !c.UseSSHConfig || c.Host == "" {
		return nil
	}
	set, err := loadSSHConfig(c.SSHConfigFile, c.isExplicit("ssh_config_file"))
	if err != nil {
		return err
	}
	if set == nil {
		return nil
	}

	alias := c.Host

	// User and port first: they are substituted into the other values.
	if !c.isExplicit("user") {
		if v := set.get(alias, "User"); v != "" {
			c.User = v
		}
	}
	if !c.isExplicit("port") {
		if v := set.get(alias, "Port"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 || n > 65535 {
				return fmt.Errorf("ssh config: invalid Port %q for %s", v, alias)
			}
			c.Port = n
		}
	}

	host := c.Host
	if v := set.get(alias, "HostName"); v != "" {
		host = expandSSHTokens(v, alias, alias, c.User, c.Port)
		if host != alias {
			c.alias = alias
			c.Host = host
		}
	}

	expand := func(s string) string {
		return expandUser(expandSSHTokens(s, alias, host, c.User, c.Port))
	}

	if !c.isExplicit("identity_files") {
		if ids := set.getAll(alias, "IdentityFile"); len(ids) > 0 {
			var files stringList
			for _, id := range ids {
				if p := expand(id); p != "" {
					files = append(files, p)
				}
			}
			// Keys named in ssh config may legitimately be absent (ssh tries
			// them and moves on), so drop the ones that are not there rather
			// than failing the connection later.
			var present stringList
			for _, p := range files {
				if _, err := os.Stat(p); err == nil {
					present = append(present, p)
				}
			}
			if len(present) > 0 {
				c.IdentityFiles = present
			}
		}
	}

	if !c.isExplicit("known_hosts_file") {
		if v := set.get(alias, "UserKnownHostsFile"); v != "" {
			// ssh accepts a list; we verify against the first readable one.
			for _, candidate := range strings.Fields(v) {
				p := expand(candidate)
				if _, err := os.Stat(p); err == nil {
					c.KnownHostsFile = p
					break
				}
			}
		}
	}

	if !c.isExplicit("insecure_ignore_host_key") {
		switch strings.ToLower(set.get(alias, "StrictHostKeyChecking")) {
		case "no", "off":
			c.InsecureHostKey = true
		}
	}

	if !c.isExplicit("connect_timeout") {
		if d, ok := sshSeconds(set.get(alias, "ConnectTimeout")); ok {
			c.ConnectTimeout = Duration(d)
		}
	}
	if !c.isExplicit("keepalive_interval") {
		if d, ok := sshSeconds(set.get(alias, "ServerAliveInterval")); ok {
			c.KeepAlive = Duration(d)
		}
	}
	return nil
}

func sshSeconds(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}
