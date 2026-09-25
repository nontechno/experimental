package main

import "testing"

func TestConfigValidation(t *testing.T) {
	base := Config{
		ListenAddress: "127.0.0.1:2049", Repository: "https://example.invalid/repo.git", Branch: "main",
		WorkDirectory: t.TempDir() + "/work", GitDirectory: t.TempDir() + "/git",
		Commit: Commit{Name: "Git NFS", Email: "gitnfs@example.invalid"},
	}
	if err := base.validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	bad := base
	bad.ListenAddress = ":2049"
	if err := bad.validate(); err == nil {
		t.Fatal("expected wildcard listener to require an ACL")
	}

	bad = base
	bad.Auth = GitAuth{Type: "ssh"}
	if err := bad.validate(); err == nil {
		t.Fatal("expected SSH key validation error")
	}

	bad = base
	bad.AllowedClients = []string{"not-a-cidr"}
	if err := bad.validate(); err == nil {
		t.Fatal("expected invalid client CIDR error")
	}
}
