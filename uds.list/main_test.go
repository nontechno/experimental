package main

import "testing"

func TestParseLine(t *testing.T) {
	cases := []struct {
		line      string
		inode     uint64
		listening bool
		kind      string
		path      string
	}{
		{"0000000000000000: 00000002 00000000 00010000 0001 01 22222 /run/foo bar.sock", 22222, true, "path", "/run/foo bar.sock"},
		{"0000000000000000: 00000003 00000000 00000000 0001 03 33333 @abstract", 33333, false, "abstract", "@abstract"},
		{"0000000000000000: 00000003 00000000 00000000 0002 03 44444", 44444, false, "unnamed", ""},
	}
	for _, c := range cases {
		s, ok := parseLine(c.line)
		if !ok {
			t.Fatalf("parse failed: %q", c.line)
		}
		if s.Inode != c.inode || s.Listening != c.listening || s.Kind != c.kind || s.Path != c.path {
			t.Errorf("got %+v for %q", s, c.line)
		}
	}
}

func TestParseSocketLink(t *testing.T) {
	if n, ok := parseSocketLink("socket:[123]"); !ok || n != 123 {
		t.Error("expected 123")
	}
	if _, ok := parseSocketLink("pipe:[123]"); ok {
		t.Error("pipe should not match")
	}
}
