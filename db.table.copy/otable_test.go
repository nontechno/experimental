package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseJDBCURL(t *testing.T) {
	cases := []struct {
		in      string
		host    string
		port    int
		service string
		sid     string
	}{
		{"jdbc:oracle:thin:@//localhost:1521/aaaaaaa", "localhost", 1521, "aaaaaaa", ""},
		{"jdbc:oracle:thin:@//db.example.com/ORCLPDB1", "db.example.com", 1521, "ORCLPDB1", ""},
		{"jdbc:oracle:thin:@db.example.com:1522/ORCLPDB1", "db.example.com", 1522, "ORCLPDB1", ""},
		{"jdbc:oracle:thin:@dbhost:1521:ORCL", "dbhost", 1521, "", "ORCL"},
		{"jdbc:oracle:thin:scott/tiger@//h:1521/svc", "h", 1521, "svc", ""},
		{"jdbc:oracle:thin:@//[::1]:1521/svc", "::1", 1521, "svc", ""},
		{"jdbc:oracle:thin:@tcps://adb.example.com:1522/svc", "adb.example.com", 1522, "svc", ""},
	}
	for _, c := range cases {
		got, err := ParseJDBCURL(c.in)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", c.in, err)
		}
		if got.Host != c.host || got.Port != c.port || got.Service != c.service || got.SID != c.sid {
			t.Errorf("%s: got host=%q port=%d service=%q sid=%q", c.in, got.Host, got.Port, got.Service, got.SID)
		}
	}
}

func TestParseJDBCURLDescriptor(t *testing.T) {
	in := `jdbc:oracle:thin:@(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=h1)(PORT=1521))(CONNECT_DATA=(SERVICE_NAME=svc)))`
	got, err := ParseJDBCURL(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Descriptor == "" {
		t.Fatal("expected descriptor to be captured")
	}
	dsn := got.BuildDSN("u", "p")
	if !strings.Contains(dsn, "connStr=") {
		t.Errorf("descriptor DSN missing connStr: %s", dsn)
	}
}

func TestParseJDBCURLErrors(t *testing.T) {
	bad := []string{
		"",
		"postgres://localhost/db",
		"jdbc:oracle:oci:@//h:1521/svc",
		"jdbc:oracle:thin:@//h:notaport/svc",
		"jdbc:oracle:thin:@//h:1521",
		"jdbc:oracle:thin:@MYALIAS",
	}
	for _, in := range bad {
		if _, err := ParseJDBCURL(in); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

func TestRedactedDSN(t *testing.T) {
	tgt, err := ParseJDBCURL("jdbc:oracle:thin:@//localhost:1521/aaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if got := tgt.Redacted("bbbb"); strings.Contains(got, "ccccocc") {
		t.Errorf("password leaked: %s", got)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "application.conf")
	content := `database {
    url = "jdbc:oracle:thin:@//localhost:1521/aaaaaaa"
    username = "bbbb"
    password = "ccccocc"
    authenticationType = "PASSWORD"
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Database.URL != "jdbc:oracle:thin:@//localhost:1521/aaaaaaa" {
		t.Errorf("url = %q", cfg.Database.URL)
	}
	if cfg.Database.Username != "bbbb" || cfg.Database.Password != "ccccocc" {
		t.Errorf("credentials = %q/%q", cfg.Database.Username, cfg.Database.Password)
	}
	if cfg.Database.AuthenticationType != "PASSWORD" {
		t.Errorf("authenticationType = %q", cfg.Database.AuthenticationType)
	}
}

func TestLoadConfigRejectsUnsupportedAuth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "application.conf")
	content := `database {
    url = "jdbc:oracle:thin:@//localhost:1521/aaaaaaa"
    username = "bbbb"
    authenticationType = "KERBEROS"
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for unsupported authenticationType")
	}
}

func TestValidateIdentifier(t *testing.T) {
	good := []string{"EMPLOYEES", "hr.employees", `HR."My Table"`, "T$1#"}
	for _, s := range good {
		if _, err := validateIdentifier(s); err != nil {
			t.Errorf("%q: unexpected error: %v", s, err)
		}
	}
	bad := []string{"", "a.b.c", "emp; DROP TABLE x", "1abc", `"unterminated`, "emp--"}
	for _, s := range bad {
		if _, err := validateIdentifier(s); err == nil {
			t.Errorf("%q: expected error", s)
		}
	}
}

func TestNumberOrString(t *testing.T) {
	if got := numberOrString(".5"); got != json.Number("0.5") {
		t.Errorf(".5 -> %#v", got)
	}
	if got := numberOrString("-.25"); got != json.Number("-0.25") {
		t.Errorf("-.25 -> %#v", got)
	}
	big := "12345678901234567890123456789012345678"
	if got := numberOrString(big); got != json.Number(big) {
		t.Errorf("big number -> %#v", got)
	}
	if got := numberOrString("abc"); got != "abc" {
		t.Errorf("abc -> %#v", got)
	}
}

func TestJSONWriter(t *testing.T) {
	var buf bytes.Buffer
	w := NewJSONWriter(&buf)
	if err := w.Begin([]string{"ID", "NAME"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Write([]any{json.Number("1"), "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Write([]any{json.Number("2"), nil}); err != nil {
		t.Fatal(err)
	}
	if err := w.End(); err != nil {
		t.Fatal(err)
	}

	var out []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(out) != 2 || out[0]["NAME"] != "alice" || out[1]["NAME"] != nil {
		t.Errorf("unexpected output: %s", buf.String())
	}
}

func TestJSONWriterEmpty(t *testing.T) {
	var buf bytes.Buffer
	w := NewJSONWriter(&buf)
	if err := w.Begin([]string{"ID"}); err != nil {
		t.Fatal(err)
	}
	if err := w.End(); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(out) != 0 {
		t.Errorf("expected empty array, got %s", buf.String())
	}
}

func TestCSVWriter(t *testing.T) {
	var buf bytes.Buffer
	w := NewCSVWriter(&buf, ',', true, "")
	if err := w.Begin([]string{"ID", "NAME"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Write([]any{json.Number("1"), "a,b"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Write([]any{json.Number("2"), nil}); err != nil {
		t.Fatal(err)
	}
	if err := w.End(); err != nil {
		t.Fatal(err)
	}
	want := "ID,NAME\n1,\"a,b\"\n2,\n"
	if buf.String() != want {
		t.Errorf("got %q want %q", buf.String(), want)
	}
}
