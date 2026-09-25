package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

func validateAuth(c *Config) error {
	if c.Repo.URL == "" {
		return nil // reported elsewhere
	}
	ep, err := transport.NewEndpoint(c.Repo.URL)
	if err != nil {
		return fmt.Errorf("repo.url: %w", err)
	}
	if ep.Password != "" {
		return errors.New("repo.url must not contain a password or token; use auth.token_file")
	}
	if ep.Protocol != "ssh" && ep.Protocol != "https" && ep.Protocol != "http" {
		return fmt.Errorf("repo.url: unsupported protocol %q (use ssh:// , user@host:path or https://)", ep.Protocol)
	}
	var errs []error
	abs := func(name, p string, required bool) {
		switch {
		case p == "" && required:
			errs = append(errs, fmt.Errorf("%s is required for auth.method %q", name, c.Auth.Method))
		case p != "" && !filepath.IsAbs(p):
			errs = append(errs, fmt.Errorf("%s must be an absolute path", name))
		}
	}
	switch c.Auth.Method {
	case "ssh":
		if ep.Protocol != "ssh" {
			errs = append(errs, fmt.Errorf("auth.method \"ssh\" needs an ssh URL, got %s://", ep.Protocol))
		}
		abs("auth.ssh_key_file", c.Auth.SSHKeyFile, true)
		abs("auth.known_hosts_file", c.Auth.KnownHostsFile, true)
		abs("auth.ssh_key_passphrase_file", c.Auth.SSHKeyPassphraseFile, false)
	case "token":
		if ep.Protocol != "https" {
			errs = append(errs, fmt.Errorf("auth.method \"token\" needs an https URL, got %s://", ep.Protocol))
		}
		if c.Auth.Username == "" {
			errs = append(errs, errors.New("auth.username is required for auth.method \"token\""))
		}
		abs("auth.token_file", c.Auth.TokenFile, true)
	case "none":
		if ep.Protocol == "ssh" {
			errs = append(errs, errors.New("ssh URLs need auth.method \"ssh\""))
		}
	case "":
		// reported as missing
	default:
		errs = append(errs, fmt.Errorf("auth.method must be \"ssh\", \"token\" or \"none\", got %q", c.Auth.Method))
	}
	abs("auth.ca_bundle_file", c.Auth.CABundleFile, false)
	if c.Auth.CABundleFile != "" && ep.Protocol != "https" {
		errs = append(errs, errors.New("auth.ca_bundle_file only applies to https URLs"))
	}
	return errors.Join(errs...)
}

// readSecret reads a secret file, refusing files that group or others can
// access. Secrets are re-read on every network operation, so rotating a
// key or token needs no restart.
func readSecret(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is accessible by group/others (mode %v); chmod 600 it", path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// authMethod builds the go-git auth for the configured method.
func authMethod(c *Config) (transport.AuthMethod, error) {
	switch c.Auth.Method {
	case "none":
		return nil, nil
	case "token":
		tok, err := readSecret(c.Auth.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("auth.token_file: %w", err)
		}
		if tok == "" {
			return nil, errors.New("auth.token_file is empty")
		}
		return &githttp.BasicAuth{Username: c.Auth.Username, Password: tok}, nil
	case "ssh":
		ep, err := transport.NewEndpoint(c.Repo.URL)
		if err != nil {
			return nil, err
		}
		user := ep.User
		if user == "" {
			user = "git"
		}
		if _, err := readSecret(c.Auth.SSHKeyFile); err != nil {
			return nil, fmt.Errorf("auth.ssh_key_file: %w", err)
		}
		pass := ""
		if c.Auth.SSHKeyPassphraseFile != "" {
			if pass, err = readSecret(c.Auth.SSHKeyPassphraseFile); err != nil {
				return nil, fmt.Errorf("auth.ssh_key_passphrase_file: %w", err)
			}
		}
		pk, err := gitssh.NewPublicKeysFromFile(user, c.Auth.SSHKeyFile, pass)
		if err != nil {
			return nil, fmt.Errorf("load ssh key: %w", err)
		}
		// Strict host key checking against an explicit known_hosts file.
		// There is deliberately no option to skip it.
		db, err := gitssh.NewKnownHostsDb(c.Auth.KnownHostsFile)
		if err != nil {
			return nil, fmt.Errorf("auth.known_hosts_file: %w", err)
		}
		port := ep.Port
		if port == 0 {
			port = 22
		}
		pk.HostKeyCallback = db.HostKeyCallback()
		pk.HostKeyAlgorithms = db.HostKeyAlgorithms(net.JoinHostPort(ep.Host, strconv.Itoa(port)))
		return pk, nil
	}
	return nil, fmt.Errorf("unknown auth.method %q", c.Auth.Method)
}

func caBundle(c *Config) ([]byte, error) {
	if c.Auth.CABundleFile == "" {
		return nil, nil
	}
	return os.ReadFile(c.Auth.CABundleFile)
}
