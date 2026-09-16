package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sample = `
database {
    url = "jdbc:oracle:thin:@//localhost:1521/aaaaaaa"
    username = "bbbb"
    password = "ccccocc"
    authenticationType = "PASSWORD"
}
`

func TestParseSample(t *testing.T) {
	cfg, literal, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if !literal {
		t.Error("expected literal password")
	}
	db := cfg.Database
	if db.Username != "bbbb" || db.Password != "ccccocc" || db.AuthenticationType != "PASSWORD" {
		t.Errorf("unexpected database block: %+v", db)
	}
	want := Target{Host: "localhost", Port: 1521, Service: "aaaaaaa"}
	if db.Target != want {
		t.Errorf("target = %+v, want %+v", db.Target, want)
	}
	if cfg.NFS.Listen != DefaultListen || cfg.Export.MaxRows != DefaultMaxRows ||
		cfg.Export.CacheTTL != DefaultCacheTTL || len(cfg.Export.Schemas) != 0 {
		t.Errorf("defaults not applied: %+v %+v", cfg.NFS, cfg.Export)
	}
}

func TestParseFull(t *testing.T) {
	t.Setenv("DBNFS_TEST_PW", "from-env")
	cfg, literal, err := Parse(`
// unrelated application settings are ignored
app.name = "svc"
database {
    url = "jdbc:oracle:thin:@db.example.com:1522:ORCL"
    username = "APP_RO"
    passwordEnv = "DBNFS_TEST_PW"
    maxConnections = 2
}
nfs { listen = "0.0.0.0:2049", allowRemote = true }
export {
    schemas = ["HR", "SALES"]
    maxRows = 0
    lobChars = 200
    cacheTTL = "5m"
    queryTimeout = "10s"
}
`)
	if err != nil {
		t.Fatal(err)
	}
	if literal {
		t.Error("password came from env, not the file")
	}
	if cfg.Database.Password != "from-env" {
		t.Errorf("password = %q", cfg.Database.Password)
	}
	if cfg.Database.Target != (Target{Host: "db.example.com", Port: 1522, SID: "ORCL"}) {
		t.Errorf("target = %+v", cfg.Database.Target)
	}
	if strings.Join(cfg.Export.Schemas, ",") != "HR,SALES" || cfg.Export.MaxRows != 0 ||
		cfg.Export.LOBChars != 200 || cfg.Export.CacheTTL != 5*time.Minute || cfg.Export.QueryTimeout != 10*time.Second {
		t.Errorf("export = %+v", cfg.Export)
	}
	if !cfg.NFS.AllowRemote || cfg.NFS.Listen != "0.0.0.0:2049" || cfg.Database.MaxConnections != 2 {
		t.Errorf("nfs/db = %+v %+v", cfg.NFS, cfg.Database)
	}
}

func TestParseSpecialCharactersInPassword(t *testing.T) {
	for text, want := range map[string]string{
		`password = """p@ss"wo\rd#//x"""`: `p@ss"wo\rd#//x`,
		`password = "p@ss\"wo\\rd#//x"`:   `p@ss"wo\rd#//x`,
		`password = "\u00e9\ud83d\ude00"`: "\u00e9\U0001F600",
	} {
		cfg, _, err := Parse("database {\n url = \"jdbc:oracle:thin:@//h/s\"\n username = \"u\"\n " + text + "\n}")
		if err != nil {
			t.Errorf("%s: %v", text, err)
			continue
		}
		if cfg.Database.Password != want {
			t.Errorf("%s: password = %q, want %q", text, cfg.Database.Password, want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	base := func(extra string) string {
		return `database { url = "jdbc:oracle:thin:@//h:1521/s", username = "u", password = "p" }` + "\n" + extra
	}
	cases := map[string]string{
		"missing url":      `database { username = "u", password = "p" }`,
		"bad auth":         base(`database.authenticationType = "KERBEROS"`),
		"remote listen":    base(`nfs.listen = "0.0.0.0:2049"`),
		"both passwords":   base(`database.passwordEnv = "X"`),
		"unset env":        `database { url = "jdbc:oracle:thin:@//h/s", username = "u", passwordEnv = "DBNFS_DEFINITELY_UNSET" }`,
		"lob too big":      base(`export.lobChars = 5000`),
		"bad duration":     base(`export.cacheTTL = "soon"`),
		"schemas not list": base(`export.schemas = "HR"`),
		"bad max rows":     base(`export.maxRows = "many"`),
		"no password":      `database { url = "jdbc:oracle:thin:@//h/s", username = "u" }`,
	}
	for name, text := range cases {
		if _, _, err := Parse(text); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLoadPermissions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "db.conf")
	if err := os.WriteFile(p, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, LoadOptions{}); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("expected permission error, got %v", err)
	}
	if _, err := Load(p, LoadOptions{InsecurePermissions: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, LoadOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestParseJDBCURL(t *testing.T) {
	good := map[string]Target{
		"jdbc:oracle:thin:@//localhost:1521/aaaaaaa": {Host: "localhost", Port: 1521, Service: "aaaaaaa"},
		"jdbc:oracle:thin:@//db/svc.example.com":     {Host: "db", Port: 1521, Service: "svc.example.com"},
		"jdbc:oracle:thin:@db:1600/SVC":              {Host: "db", Port: 1600, Service: "SVC"},
		"jdbc:oracle:thin:@db:1600:ORCL":             {Host: "db", Port: 1600, SID: "ORCL"},
		"jdbc:oracle:thin:@//[::1]:1521/svc":         {Host: "::1", Port: 1521, Service: "svc"},
		"JDBC:ORACLE:THIN:@//10.0.0.5:1521/svc":      {Host: "10.0.0.5", Port: 1521, Service: "svc"},
	}
	for in, want := range good {
		got, err := ParseJDBCURL(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %+v want %+v", in, got, want)
		}
	}
	bad := []string{
		"",
		"oracle://u:p@h/s",
		"jdbc:oracle:thin:@",
		"jdbc:oracle:thin:@(DESCRIPTION=(ADDRESS=(HOST=h)))",
		"jdbc:oracle:thin:@//h:1521",
		"jdbc:oracle:thin:@//h:99999/s",
		"jdbc:oracle:thin:@//h:1521/",
		"jdbc:oracle:thin:@//h:1521/s?x=y",
		"jdbc:oracle:thin:@h",
		"jdbc:oracle:thin:@//:1521/s",
		"jdbc:oracle:thin:@//[::1/s",
	}
	for _, in := range bad {
		if got, err := ParseJDBCURL(in); err == nil {
			t.Errorf("%q: expected error, got %+v", in, got)
		}
	}
}
