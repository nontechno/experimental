package main

import (
	"database/sql"
	"os"
	"strings"
	"testing"
)

func TestParseConfigExample(t *testing.T) {
	src := `
database {
	url = "jdbc:oracle:thin:@//localhost:1521/aaaaaaa"
	username = "bbbb"
	password = "ccccocc"
	authenticationType = "PASSWORD"
}
`
	root, err := ParseConfig(src, "test.conf")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for path, want := range map[string]string{
		"database.url":                "jdbc:oracle:thin:@//localhost:1521/aaaaaaa",
		"database.username":           "bbbb",
		"database.password":           "ccccocc",
		"database.authenticationType": "PASSWORD",
	} {
		got, ok := root.GetString(path)
		if !ok || got != want {
			t.Errorf("%s = %q (ok=%v), want %q", path, got, ok, want)
		}
	}
}

func TestParseConfigVariants(t *testing.T) {
	src := `
# a comment
database {
    url : "jdbc:oracle:thin:@//db:1521/svc"   // trailing comment
    username = scott                          # bare value
    password = "p@ss{}word"
    options {
        "TIMEOUT" = "30"
        TRACE FILE = "/tmp/t.log"
    }
}
database.schema = "HR"
/* block
   comment */
logging.level = DEBUG
`
	root, err := ParseConfig(src, "test.conf")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v, _ := root.GetString("database.username"); v != "scott" {
		t.Errorf("username = %q", v)
	}
	if v, _ := root.GetString("database.password"); v != "p@ss{}word" {
		t.Errorf("password = %q", v)
	}
	if v, _ := root.GetString("database.schema"); v != "HR" {
		t.Errorf("dotted key merge failed: schema = %q", v)
	}
	if v, _ := root.GetString("database.url"); v != "jdbc:oracle:thin:@//db:1521/svc" {
		t.Errorf("url = %q", v)
	}
	opts, ok := root.GetObject("database.options")
	if !ok {
		t.Fatal("missing options block")
	}
	if m := opts.StringMap(); m["TIMEOUT"] != "30" || m["TRACE FILE"] != "/tmp/t.log" {
		t.Errorf("options = %v", m)
	}
	if v, _ := root.GetString("logging.level"); v != "DEBUG" {
		t.Errorf("logging.level = %q", v)
	}
}

func TestParseConfigCaseInsensitiveLookup(t *testing.T) {
	root, err := ParseConfig(`database { AuthenticationType = "PASSWORD" }`, "t")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := root.GetString("database.authenticationType"); !ok || v != "PASSWORD" {
		t.Errorf("case-insensitive lookup failed: %q %v", v, ok)
	}
}

func TestParseConfigEnvSubstitution(t *testing.T) {
	t.Setenv("ORASCHEMA_TEST_PW", "s3cret")
	root, err := ParseConfig(`database { password = ${ORASCHEMA_TEST_PW}, extra = "x${?NOPE_NOT_SET}y" }`, "t")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := root.GetString("database.password"); v != "s3cret" {
		t.Errorf("password = %q", v)
	}
	if v, _ := root.GetString("database.extra"); v != "xy" {
		t.Errorf("extra = %q", v)
	}

	if _, err := ParseConfig(`a = ${DEFINITELY_NOT_SET_12345}`, "t"); err == nil {
		t.Error("expected an error for an unset mandatory variable")
	}
}

func TestParseConfigErrors(t *testing.T) {
	for name, src := range map[string]string{
		"unclosed brace":   `database { url = "x"`,
		"unterminated str": `a = "abc`,
		"missing sep":      `database url`,
		"stray brace":      `}`,
	} {
		if _, err := ParseConfig(src, "t"); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestLoadDatabaseConfig(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "*.conf")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`database {
	url = "jdbc:oracle:thin:@//localhost:1521/aaaaaaa"
	username = "bbbb"
	password = "ccccocc"
	authenticationType = "PASSWORD"
}`)
	f.Close()

	cfg, err := LoadDatabaseConfig(f.Name())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Username != "bbbb" || cfg.Password != "ccccocc" || cfg.AuthenticationType != "PASSWORD" {
		t.Errorf("bad config: %+v", cfg)
	}

	target, dsn, err := BuildDSN(cfg)
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	if target.Host != "localhost" || target.Port != 1521 || target.Service != "aaaaaaa" {
		t.Errorf("target = %+v", target)
	}
	if !strings.Contains(dsn, "localhost:1521") || !strings.Contains(dsn, "aaaaaaa") {
		t.Errorf("dsn = %q", dsn)
	}
	if strings.Contains(RedactDSN(dsn), "ccccocc") {
		t.Errorf("password leaked through RedactDSN: %q", RedactDSN(dsn))
	}
}

func TestParseJDBCURL(t *testing.T) {
	tests := []struct {
		url          string
		host         string
		port         int
		service, sid string
		tcps         bool
	}{
		{url: "jdbc:oracle:thin:@//localhost:1521/aaaaaaa", host: "localhost", port: 1521, service: "aaaaaaa"},
		{url: "jdbc:oracle:thin:@//db.example.com/orclpdb", host: "db.example.com", port: 1521, service: "orclpdb"},
		{url: "jdbc:oracle:thin:@dbhost:1522/svc", host: "dbhost", port: 1522, service: "svc"},
		{url: "jdbc:oracle:thin:@dbhost:1521:ORCL", host: "dbhost", port: 1521, sid: "ORCL"},
		{url: "JDBC:ORACLE:THIN:@//UPPER:1521/S", host: "UPPER", port: 1521, service: "S"},
		{url: "jdbc:oracle:thin:@//[::1]:1521/svc", host: "::1", port: 1521, service: "svc"},
		{url: "jdbc:oracle:oci:@//h:1521/s", host: "h", port: 1521, service: "s"},
		{
			url:  "jdbc:oracle:thin:@(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=tns.example.com)(PORT=1530))(CONNECT_DATA=(SERVICE_NAME=pdb1)))",
			host: "tns.example.com", port: 1530, service: "pdb1",
		},
		{
			url:  "jdbc:oracle:thin:@(DESCRIPTION=(ADDRESS=(PROTOCOL=TCPS)(HOST=sec.example.com)(PORT=2484))(CONNECT_DATA=(SID=ORCL)))",
			host: "sec.example.com", port: 2484, sid: "ORCL", tcps: true,
		},
	}
	for _, tc := range tests {
		got, err := ParseJDBCURL(tc.url)
		if err != nil {
			t.Errorf("%s: %v", tc.url, err)
			continue
		}
		if got.Host != tc.host || got.Port != tc.port || got.Service != tc.service || got.SID != tc.sid || got.TCPS != tc.tcps {
			t.Errorf("%s:\n got %+v\nwant host=%s port=%d service=%s sid=%s tcps=%v",
				tc.url, got, tc.host, tc.port, tc.service, tc.sid, tc.tcps)
		}
	}
}

func TestParseJDBCURLErrors(t *testing.T) {
	for _, url := range []string{
		"postgres://localhost/db",
		"jdbc:oracle:thin:@myalias",
		"jdbc:oracle:thin://localhost:1521/svc", // no '@'
		"jdbc:oracle:thin:@//localhost:notaport/svc",
		"jdbc:oracle:thin:@",
		"jdbc:oracle:thin:@(DESCRIPTION=(ADDRESS=(PORT=1521)))",
	} {
		if got, err := ParseJDBCURL(url); err == nil {
			t.Errorf("%s: expected an error, got %+v", url, got)
		}
	}
}

func TestRenderType(t *testing.T) {
	i := func(v int64) nullInt { return nullInt{Int64: v, Valid: true} }
	s := func(v string) nullStr { return nullStr{String: v, Valid: true} }
	var noInt nullInt
	var noStr nullStr

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"varchar2 char", renderType("VARCHAR2", i(200), noInt, noInt, i(50), s("C")), "VARCHAR2(50 CHAR)"},
		{"varchar2 byte", renderType("VARCHAR2", i(50), noInt, noInt, i(50), s("B")), "VARCHAR2(50 BYTE)"},
		{"number p,s", renderType("NUMBER", i(22), i(10), i(2), noInt, noStr), "NUMBER(10,2)"},
		{"number p", renderType("NUMBER", i(22), i(6), i(0), noInt, noStr), "NUMBER(6)"},
		{"number free", renderType("NUMBER", i(22), noInt, noInt, noInt, noStr), "NUMBER"},
		{"raw", renderType("RAW", i(16), noInt, noInt, noInt, noStr), "RAW(16)"},
		{"timestamp", renderType("TIMESTAMP(6) WITH TIME ZONE", i(13), noInt, i(6), noInt, noStr), "TIMESTAMP(6) WITH TIME ZONE"},
		{"clob", renderType("CLOB", i(4000), noInt, noInt, noInt, noStr), "CLOB"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// Aliases keeping the renderType test table readable.
type nullInt = sql.NullInt64
type nullStr = sql.NullString
