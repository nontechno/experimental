package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const baseConfig = `
[repo]
url = "git@example.com:org/repo.git"
branch = "main"
[mount]
path = "/srv/repo"
[state]
dir = "/var/lib/gitmount/repo"
[commit]
author_name = "gitmount"
author_email = "gitmount@example.com"
[auth]
method = "ssh"
ssh_key_file = "/k"
known_hosts_file = "/kh"
`

func loadString(t *testing.T, s string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadConfig(p)
}

func TestConfigValid(t *testing.T) {
	c, err := loadString(t, baseConfig)
	if err != nil {
		t.Fatal(err)
	}
	if c.Commit.QuietPeriod.Seconds() != 2 || !c.Sync.Push {
		t.Errorf("defaults not applied: %+v", c)
	}
}

func TestConfigErrors(t *testing.T) {
	tests := map[string]struct{ from, to, want string }{
		"unknown key":      {"[auth]", "typo = 1\n[auth]", "unknown keys"},
		"relative mount":   {`"/srv/repo"`, `"srv/repo"`, "mount.path must be an absolute path"},
		"nested state":     {`"/var/lib/gitmount/repo"`, `"/srv/repo/state"`, "must not contain each other"},
		"token in url":     {`"git@example.com:org/repo.git"`, `"https://u:tok@example.com/r.git"`, "must not contain a password"},
		"ssh auth + https": {`"git@example.com:org/repo.git"`, `"https://example.com/r.git"`, `needs an ssh URL`},
		"bad method":       {`method = "ssh"`, `method = "pw"`, `auth.method must be`},
		"missing key":      {`ssh_key_file = "/k"`, ``, "auth.ssh_key_file is required"},
		"file url":         {`"git@example.com:org/repo.git"`, `"file:///tmp/r.git"`, "unsupported protocol"},
		"max<quiet":        {"[commit]", "[commit]\nmax_delay = \"1s\"", "commit.max_delay must be >="},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := loadString(t, strings.Replace(baseConfig, tc.from, tc.to, 1))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestExampleConfigLoads(t *testing.T) {
	if _, err := LoadConfig("gitmount.example.toml"); err != nil {
		t.Fatal(err)
	}
}
