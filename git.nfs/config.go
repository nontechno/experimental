package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ListenAddress  string   `yaml:"listen_address"`
	AllowedClients []string `yaml:"allowed_clients"`
	Repository     string   `yaml:"repository"`
	Branch         string   `yaml:"branch"`
	WorkDirectory  string   `yaml:"work_directory"`
	GitDirectory   string   `yaml:"git_directory"`
	Auth           GitAuth  `yaml:"auth"`
	Commit         Commit   `yaml:"commit"`
}

type GitAuth struct {
	Type           string `yaml:"type"` // none, https, ssh
	Username       string `yaml:"username"`
	Password       string `yaml:"password"`
	Token          string `yaml:"token"`
	SSHUser        string `yaml:"ssh_user"`
	PrivateKeyFile string `yaml:"private_key_file"`
	KnownHostsFile string `yaml:"known_hosts_file"`
	Passphrase     string `yaml:"passphrase"`
}

type Commit struct {
	Name    string `yaml:"name"`
	Email   string `yaml:"email"`
	Message string `yaml:"message"`
}

func loadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	var c Config
	d := yaml.NewDecoder(f)
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, errors.New("config must contain exactly one YAML document")
		}
		return Config{}, fmt.Errorf("decode trailing config: %w", err)
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	if c.ListenAddress == "" {
		return errors.New("listen_address is required")
	}
	if _, _, err := net.SplitHostPort(c.ListenAddress); err != nil {
		return fmt.Errorf("invalid listen_address: %w", err)
	}
	host, _, _ := net.SplitHostPort(c.ListenAddress)
	listenIP := net.ParseIP(strings.Trim(host, "[]"))
	if (host == "" || (listenIP != nil && !listenIP.IsLoopback()) || (listenIP == nil && host != "localhost")) && len(c.AllowedClients) == 0 {
		return errors.New("allowed_clients is required when listen_address is not loopback")
	}
	if c.Repository == "" {
		return errors.New("repository is required")
	}
	if c.Branch == "" || strings.HasPrefix(c.Branch, "-") {
		return errors.New("branch is required and must not begin with '-'")
	}
	if c.WorkDirectory == "" || c.GitDirectory == "" {
		return errors.New("work_directory and git_directory are required")
	}
	work, err := filepath.Abs(c.WorkDirectory)
	if err != nil {
		return err
	}
	gitDir, err := filepath.Abs(c.GitDirectory)
	if err != nil {
		return err
	}
	if work == gitDir || within(work, gitDir) || within(gitDir, work) {
		return errors.New("work_directory and git_directory must be separate, non-nested directories")
	}
	if c.Commit.Name == "" || c.Commit.Email == "" {
		return errors.New("commit.name and commit.email are required")
	}
	switch c.Auth.Type {
	case "", "none":
	case "https":
		if c.Auth.Token == "" && (c.Auth.Username == "" || c.Auth.Password == "") {
			return errors.New("https auth requires token or username and password")
		}
	case "ssh":
		if c.Auth.PrivateKeyFile == "" || c.Auth.KnownHostsFile == "" {
			return errors.New("ssh auth requires private_key_file and known_hosts_file")
		}
	default:
		return fmt.Errorf("unsupported auth.type %q (expected none, https, or ssh)", c.Auth.Type)
	}
	for _, cidr := range c.AllowedClients {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid allowed_clients entry %q: %w", cidr, err)
		}
	}
	return nil
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}
