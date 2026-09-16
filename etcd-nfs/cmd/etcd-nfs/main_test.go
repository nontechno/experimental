package main

import (
	"net"
	"strings"
	"testing"
)

func TestSplitEndpoints(t *testing.T) {
	got := splitEndpoints(" http://10.0.0.1:2379/, https://etcd:2379 ,,")
	if strings.Join(got, "|") != "http://10.0.0.1:2379|https://etcd:2379" {
		t.Fatalf("got %q", got)
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]uint32{"0644": 0o644, "755": 0o755, "0": 0} {
		m, err := parseMode("file-mode", in)
		if err != nil || uint32(m) != want {
			t.Fatalf("%s: got %o, %v", in, m, err)
		}
	}
	for _, bad := range []string{"999", "1777", "rw-r--r--", ""} {
		if _, err := parseMode("file-mode", bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestMountHint(t *testing.T) {
	addr := &net.TCPAddr{IP: net.IPv4zero, Port: 12049}
	if h := mountHint(addr); h != "" && !strings.Contains(h, "SERVER:/") {
		t.Fatalf("unspecified address should use a placeholder: %s", h)
	}
}

func TestIsMacOSMetadata(t *testing.T) {
	for name, want := range map[string]bool{".DS_Store": true, "._x": true, "x": false, ".env": false} {
		if isMacOSMetadata(name) != want {
			t.Fatalf("%s", name)
		}
	}
}
