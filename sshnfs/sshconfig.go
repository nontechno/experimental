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
	var (
		set *sshConfigSet
		err error
	)
	if c.UseSSHConfig && c.Host != "" {
		if set, err = loadSSHConfig(c.SSHConfigFile, c.isExplicit("ssh_config_file")); err != nil {
			return err
		}
	}
	if set == nil {
		// Still resolve a ProxyJump given by flag or config file.
		return c.resolveProxy(nil)
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

	if !c.isExplicit("proxy_jump") {
		if v := set.get(alias, "ProxyJump"); v != "" {
			c.ProxyJump = v
		}
	}
	if !c.isExplicit("proxy_command") {
		if v := set.get(alias, "ProxyCommand"); v != "" {
			c.ProxyCommand = expandSSHTokens(v, alias, host, c.User, c.Port)
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
	return c.resolveProxy(set)
}

// maxJumpHops and maxJumpDepth bound both the chain and the recursion used to
// expand it, so a mistyped config cannot spin forever.
const (
	maxJumpHops  = 8
	maxJumpDepth = 8
)

// resolveProxy turns the ProxyJump string into fully specified hops. Each hop
// is itself looked up in ssh config, so "-J bastion" picks up the HostName,
// Port, User and IdentityFile you already have for "bastion"; whatever is
// still missing falls back to the target's own settings. A hop that has its
// own ProxyJump is expanded in place, which is how a bastion behind another
// bastion works without spelling out the whole chain.
func (c *Config) resolveProxy(set *sshConfigSet) error {
	if isNone(c.ProxyJump) {
		c.ProxyJump, c.jumpHops = "", nil
		if isNone(c.ProxyCommand) {
			c.ProxyCommand = ""
		}
		return nil
	}
	if isNone(c.ProxyCommand) {
		c.ProxyCommand = ""
	}
	hops, err := c.expandJumpSpec(set, c.ProxyJump, nil, 0)
	if err != nil {
		return err
	}
	if len(hops) > maxJumpHops {
		return fmt.Errorf("proxy_jump %q: %d hops, more than the %d supported", c.ProxyJump, len(hops), maxJumpHops)
	}
	c.jumpHops = hops
	return nil
}

// expandJumpSpec parses a jump list and resolves every entry, recursing into
// each hop's own ProxyJump. path carries the aliases already being expanded so
// a cycle is reported rather than followed.
func (c *Config) expandJumpSpec(set *sshConfigSet, spec string, path []string, depth int) ([]*endpoint, error) {
	if depth > maxJumpDepth {
		return nil, fmt.Errorf("proxy jump %q: nested more than %d deep", spec, maxJumpDepth)
	}
	parsed, err := parseJumpSpec(spec)
	if err != nil {
		return nil, err
	}

	var out []*endpoint
	for _, hop := range parsed {
		alias := hop.Host
		for _, seen := range path {
			if seen == alias {
				return nil, fmt.Errorf("proxy jump: %s is reached through itself (%s)",
					alias, strings.Join(append(path, alias), " -> "))
			}
		}

		// The hop's own proxy settings decide how we get to it.
		if nested := set.get(alias, "ProxyJump"); nested != "" && !isNone(nested) {
			sub, err := c.expandJumpSpec(set, nested, append(append([]string{}, path...), alias), depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
		} else if pc := set.get(alias, "ProxyCommand"); pc != "" && !isNone(pc) {
			hop.ProxyCommand = pc
		}

		if err := c.fillHop(set, alias, hop); err != nil {
			return nil, err
		}
		out = append(out, hop)
		if len(out) > maxJumpHops {
			return nil, fmt.Errorf("proxy jump %q: more than %d hops", spec, maxJumpHops)
		}
	}
	return out, nil
}

// fillHop completes one hop from ssh config, then from the target's settings.
func (c *Config) fillHop(set *sshConfigSet, alias string, hop *endpoint) error {
	if hop.User == "" {
		if v := set.get(alias, "User"); v != "" {
			hop.User = v
		}
	}
	if hop.Port == 0 {
		if v := set.get(alias, "Port"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 || n > 65535 {
				return fmt.Errorf("ssh config: invalid Port %q for jump host %s", v, alias)
			}
			hop.Port = n
		}
	}
	if v := set.get(alias, "HostName"); v != "" {
		hop.Host = expandSSHTokens(v, alias, alias, hop.User, hop.Port)
	}
	for _, id := range set.getAll(alias, "IdentityFile") {
		p := expandUser(expandSSHTokens(id, alias, hop.Host, hop.User, hop.Port))
		if _, err := os.Stat(p); err == nil {
			hop.IdentityFiles = append(hop.IdentityFiles, p)
		}
	}
	if strings.EqualFold(set.get(alias, "StrictHostKeyChecking"), "no") {
		hop.Insecure = true
	}

	// Fall back to the target's settings for anything ssh config did not
	// answer, which is what makes "-J bastion" work with no extra setup.
	if hop.User == "" {
		hop.User = c.User
	}
	if hop.Port == 0 {
		hop.Port = 22
	}
	if len(hop.IdentityFiles) == 0 {
		hop.IdentityFiles = c.IdentityFiles
	}
	if hop.ProxyCommand != "" {
		hop.ProxyCommand = expandSSHTokens(hop.ProxyCommand, alias, hop.Host, hop.User, hop.Port)
	}
	hop.Passphrase = c.IdentityPassphrase
	hop.UseAgent = c.UseAgent
	hop.KnownHostsFile = c.KnownHostsFile
	if c.InsecureHostKey {
		hop.Insecure = true
	}
	hop.ConnectTimeout = c.ConnectTimeout.D()
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
