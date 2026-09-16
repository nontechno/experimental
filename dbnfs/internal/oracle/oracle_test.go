package oracle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	go_ora "github.com/sijms/go-ora/v2"

	"dbnfs/internal/config"
	"dbnfs/internal/dbfs"
)

// ---- fake driver ----

type fakeResult struct {
	cols []string
	rows [][]any
	err  error
}

type fakeDB struct {
	mu      sync.Mutex
	log     []string // statements and tx events, in order
	args    [][]driver.NamedValue
	respond func(query string) fakeResult
}

func (f *fakeDB) record(s string, args []driver.NamedValue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, s)
	f.args = append(f.args, args)
}

func (f *fakeDB) events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

type fakeConnector struct{ db *fakeDB }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) { return &fakeConn{db: c.db}, nil }
func (c fakeConnector) Driver() driver.Driver                        { return nil }

type fakeConn struct{ db *fakeDB }

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) { return &fakeStmt{db: c.db, q: q}, nil }
func (c *fakeConn) Close() error                          { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)             { c.db.record("BEGIN", nil); return &fakeTx{c.db}, nil }

type fakeTx struct{ db *fakeDB }

func (t *fakeTx) Commit() error   { t.db.record("COMMIT", nil); return nil }
func (t *fakeTx) Rollback() error { t.db.record("ROLLBACK", nil); return nil }

type fakeStmt struct {
	db *fakeDB
	q  string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }
func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.db.record(normalize(s.q), named(args))
	if r := s.db.respond(s.q); r.err != nil {
		return nil, r.err
	}
	return driver.RowsAffected(0), nil
}
func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.db.record(normalize(s.q), named(args))
	r := s.db.respond(s.q)
	if r.err != nil {
		return nil, r.err
	}
	return &fakeRows{cols: r.cols, rows: r.rows}, nil
}

func named(args []driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, len(args))
	for i, a := range args {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: a}
	}
	return out
}

type fakeRows struct {
	cols []string
	rows [][]any
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	for j, v := range r.rows[r.i] {
		dest[j] = v
	}
	r.i++
	return nil
}

func normalize(q string) string { return strings.Join(strings.Fields(q), " ") }

func newFakeSource(t *testing.T, ex config.Export, respond func(q string) fakeResult) (*Source, *fakeDB) {
	t.Helper()
	fdb := &fakeDB{respond: respond}
	db := sql.OpenDB(fakeConnector{fdb})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if ex.MaxObjects == 0 {
		ex.MaxObjects = 1000
	}
	return New(db, ex, slog.New(slog.NewTextHandler(io.Discard, nil))), fdb
}

func contains(q, sub string) bool { return strings.Contains(normalize(q), sub) }

// ---- tests ----

func TestConnectionURLRoundTrip(t *testing.T) {
	for _, d := range []config.Database{
		{Username: "APP_RO", Password: `p@ss:w/o?rd#%&"`, Target: config.Target{Host: "db.example.com", Port: 1521, Service: "FREEPDB1"}},
		{Username: "u", Password: "x", Target: config.Target{Host: "::1", Port: 1600, SID: "ORCL"}},
	} {
		u := connectionURL(d)
		cfg, err := go_ora.ParseConfig(u)
		if err != nil {
			t.Fatalf("%+v: %v", d.Target, err)
		}
		if cfg.UserID != d.Username || cfg.Password != d.Password {
			t.Errorf("credentials did not round-trip: user %q password %q", cfg.UserID, cfg.Password)
		}
		if len(cfg.Servers) != 1 || cfg.Servers[0].Addr != d.Target.Host || cfg.Servers[0].Port != d.Target.Port {
			t.Errorf("server = %+v, want %s:%d", cfg.Servers, d.Target.Host, d.Target.Port)
		}
		if cfg.ServiceName != d.Target.Service || cfg.SID != d.Target.SID {
			t.Errorf("service %q sid %q, want %q %q", cfg.ServiceName, cfg.SID, d.Target.Service, d.Target.SID)
		}
		if strings.Contains(fmt.Sprint(d.Target), d.Password) {
			t.Error("target string leaks password")
		}
	}
}

func TestOpenRefusesSYS(t *testing.T) {
	_, err := Open(context.Background(), config.Database{Username: "sys", Password: "x",
		Target: config.Target{Host: "127.0.0.1", Port: 1, Service: "s"}, MaxConnections: 1})
	if err == nil || !strings.Contains(err.Error(), "SYS") {
		t.Fatalf("err = %v", err)
	}
}

func TestQuoteIdent(t *testing.T) {
	if q, err := quoteIdent(`My Table.x/y`); err != nil || q != `"My Table.x/y"` {
		t.Errorf("got %q %v", q, err)
	}
	for _, bad := range []string{"", `a"b`, "a\x00b", strings.Repeat("x", 600)} {
		if _, err := quoteIdent(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestTextExpr(t *testing.T) {
	c := `"C"`
	cases := map[string]string{
		"VARCHAR2":                          `"C"`,
		"NUMBER":                            `TO_CHAR("C", 'TM9', 'NLS_NUMERIC_CHARACTERS=''.,''')`,
		"DATE":                              `TO_CHAR("C", 'YYYY-MM-DD"T"HH24:MI:SS')`,
		"TIMESTAMP(6)":                      `TO_CHAR("C", 'YYYY-MM-DD"T"HH24:MI:SS.FF')`,
		"TIMESTAMP(0)":                      `TO_CHAR("C", 'YYYY-MM-DD"T"HH24:MI:SS')`,
		"TIMESTAMP(3) WITH TIME ZONE":       `TO_CHAR("C", 'YYYY-MM-DD"T"HH24:MI:SS.FFTZH:TZM')`,
		"TIMESTAMP(6) WITH LOCAL TIME ZONE": `TO_CHAR("C", 'YYYY-MM-DD"T"HH24:MI:SS.FF')`,
		"INTERVAL DAY(2) TO SECOND(6)":      `TO_CHAR("C")`,
		"RAW":                               `RAWTOHEX("C")`,
		"CLOB":                              `DBMS_LOB.SUBSTR("C", 500, 1)`,
		"BLOB":                              `CASE WHEN "C" IS NULL THEN NULL ELSE '<BLOB ' || TO_CHAR(DBMS_LOB.GETLENGTH("C")) || ' bytes>' END`,
		"LONG":                              `'<LONG>'`,
		"XMLTYPE":                           `'<XMLTYPE>'`,
		"EVIL'); DROP TABLE x; --":          `'<EVIL) DROP TABLE x >'`,
	}
	for dt, want := range cases {
		if got := textExpr(c, dt, 500); got != want {
			t.Errorf("%s:\n got %s\nwant %s", dt, got, want)
		}
	}
	if got := textExpr(c, "NCLOB", 0); got != `CASE WHEN "C" IS NULL THEN NULL ELSE '<NCLOB>' END` {
		t.Errorf("NCLOB with lobChars=0: %s", got)
	}
}

func TestEveryStatementIsReadOnlyAndRolledBack(t *testing.T) {
	src, fdb := newFakeSource(t, config.Export{Schemas: []string{"HR", "SALES"}, MaxRows: 10, MaxObjects: 100},
		func(q string) fakeResult {
			switch {
			case contains(q, "FROM all_users"):
				return fakeResult{cols: []string{"USERNAME"}, rows: [][]any{{"HR"}, {"SALES"}}}
			case contains(q, "FROM all_tables"):
				return fakeResult{cols: []string{"OWNER", "TABLE_NAME"}, rows: [][]any{{"HR", "EMP"}}}
			}
			return fakeResult{cols: []string{"X"}}
		})
	ctx := context.Background()
	if s, err := src.Schemas(ctx); err != nil || strings.Join(s, ",") != "HR,SALES" {
		t.Fatalf("schemas = %v %v", s, err)
	}
	if objs, err := src.Objects(ctx, "table"); err != nil || len(objs) != 1 || objs[0] != (dbfs.Object{Owner: "HR", Name: "EMP"}) {
		t.Fatalf("objects = %v %v", objs, err)
	}

	ev := fdb.events()
	if len(ev) != 8 {
		t.Fatalf("events = %q", ev)
	}
	for i := 0; i < len(ev); i += 4 {
		if ev[i] != "BEGIN" || ev[i+1] != "SET TRANSACTION READ ONLY" || ev[i+3] != "ROLLBACK" {
			t.Errorf("transaction %d = %q", i/4, ev[i:i+4])
		}
	}
	// Configured schema names are bound, never concatenated.
	if !strings.Contains(ev[2], "username IN (:s0, :s1)") || strings.Contains(ev[2], "'HR'") {
		t.Errorf("schema filter = %s", ev[2])
	}
	fdb.mu.Lock()
	args := fdb.args[2]
	fdb.mu.Unlock()
	if len(args) != 2 || args[0].Value != "HR" || args[1].Value != "SALES" {
		t.Errorf("binds = %v", args)
	}
}

func TestDefaultSchemaFilter(t *testing.T) {
	src, _ := newFakeSource(t, config.Export{}, func(string) fakeResult { return fakeResult{} })
	f, args := src.ownerFilter("owner")
	if f != "owner IN (SELECT username FROM all_users WHERE oracle_maintained = 'N')" || args != nil {
		t.Errorf("%s %v", f, args)
	}
}

func TestObjectsCap(t *testing.T) {
	src, _ := newFakeSource(t, config.Export{MaxObjects: 2}, func(q string) fakeResult {
		return fakeResult{cols: []string{"OWNER", "NAME"}, rows: [][]any{{"A", "1"}, {"A", "2"}, {"A", "3"}}}
	})
	objs, err := src.Objects(context.Background(), "view")
	if err != nil || len(objs) != 2 {
		t.Errorf("objects = %v %v", objs, err)
	}
}

func TestRowsCSV(t *testing.T) {
	var dataQuery string
	src, fdb := newFakeSource(t, config.Export{MaxRows: 2, LOBChars: 100}, func(q string) fakeResult {
		switch {
		case contains(q, "SELECT column_name, data_type FROM all_tab_columns"):
			return fakeResult{cols: []string{"COLUMN_NAME", "DATA_TYPE"}, rows: [][]any{
				{"ID", "NUMBER"}, {"Note, with comma", "VARCHAR2"}, {"DOC", "CLOB"},
			}}
		case contains(q, `FROM "HR"."EMP"`):
			dataQuery = normalize(q)
			return fakeResult{cols: []string{"A", "B", "C"}, rows: [][]any{
				{"1", "hello, \"world\"\nline2", nil},
				{"2", nil, "doc"},
			}}
		}
		return fakeResult{}
	})
	b, err := src.ObjectFile(context.Background(), "table", dbfs.Object{Owner: "HR", Name: "EMP"}, "rows.csv", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	want := "ID,\"Note, with comma\",DOC\n1,\"hello, \"\"world\"\"\nline2\",\n2,,doc\n"
	if string(b) != want {
		t.Errorf("csv:\n%q\nwant\n%q", b, want)
	}
	if !strings.Contains(dataQuery, `WHERE ROWNUM <= :max_rows`) || !strings.Contains(dataQuery, `DBMS_LOB.SUBSTR("DOC", 100, 1)`) {
		t.Errorf("query = %s", dataQuery)
	}
	fdb.mu.Lock()
	last := fdb.args[len(fdb.args)-2] // data query precedes ROLLBACK
	fdb.mu.Unlock()
	if len(last) != 1 || last[0].Value != int64(2) {
		t.Errorf("max_rows bind = %v", last)
	}

	// Byte limit stops at a row boundary.
	small, err := src.ObjectFile(context.Background(), "table", dbfs.Object{Owner: "HR", Name: "EMP"}, "rows.csv", 40)
	if err != nil {
		t.Fatal(err)
	}
	if string(small) != "ID,\"Note, with comma\",DOC\n" {
		t.Errorf("limited csv = %q", small)
	}
}

func TestRowsCSVRejectsQuoteInIdentifier(t *testing.T) {
	src, _ := newFakeSource(t, config.Export{MaxRows: 1}, func(q string) fakeResult {
		return fakeResult{cols: []string{"COLUMN_NAME", "DATA_TYPE"}, rows: [][]any{{`x" FROM dual; --`, "VARCHAR2"}}}
	})
	_, err := src.ObjectFile(context.Background(), "view", dbfs.Object{Owner: "HR", Name: "V"}, "rows.csv", 1<<20)
	if err == nil || !strings.Contains(err.Error(), "refusing to quote") {
		t.Errorf("err = %v", err)
	}
}

func TestTSVAndKV(t *testing.T) {
	src, _ := newFakeSource(t, config.Export{MaxRows: 1}, func(q string) fakeResult {
		switch {
		case contains(q, "FROM all_tab_columns"):
			return fakeResult{cols: []string{"COLUMN_ID", "COLUMN_NAME"}, rows: [][]any{{"1", "a\tb\\c\nd"}, {"2", nil}}}
		case contains(q, "FROM all_constraints"):
			return fakeResult{cols: []string{"CONSTRAINT_NAME"}} // no rows
		case contains(q, "FROM all_sequences"):
			return fakeResult{cols: []string{"MIN_VALUE", "CYCLE_FLAG"}, rows: [][]any{{"1", "N"}}}
		case contains(q, "FROM all_synonyms"):
			return fakeResult{cols: []string{"TABLE_OWNER"}}
		}
		return fakeResult{}
	})
	ctx := context.Background()
	o := dbfs.Object{Owner: "HR", Name: "EMP"}
	b, err := src.ObjectFile(ctx, "table", o, "columns.tsv", 1<<20)
	if err != nil || string(b) != "column_id\tcolumn_name\n1\ta\\tb\\\\c\\nd\n2\t\n" {
		t.Errorf("columns.tsv = %q %v", b, err)
	}
	b, err = src.ObjectFile(ctx, "table", o, "constraints.tsv", 1<<20)
	if err != nil || string(b) != "constraint_name\n" {
		t.Errorf("empty tsv = %q %v", b, err)
	}
	b, err = src.ObjectFile(ctx, "sequence", o, "info.txt", 1<<20)
	if err != nil || string(b) != "min_value:   1\ncycle_flag:  N\n" {
		t.Errorf("info.txt = %q %v", b, err)
	}
	if _, err = src.ObjectFile(ctx, "synonym", o, "info.txt", 1<<20); !errors.Is(err, errNoRows) {
		t.Errorf("missing synonym err = %v", err)
	}
	if _, err = src.ObjectFile(ctx, "table", o, "nope.txt", 1<<20); err == nil {
		t.Error("unknown file accepted")
	}
}

func TestSourceAndDDL(t *testing.T) {
	var roAttempts, rwAttempts int
	src, fdb := newFakeSource(t, config.Export{MaxRows: 1}, func(q string) fakeResult {
		switch {
		case contains(q, "FROM all_source"):
			return fakeResult{cols: []string{"TEXT"}, rows: [][]any{{"PROCEDURE p IS\n"}, {"BEGIN NULL; END;\n"}}}
		case contains(q, "DBMS_METADATA.GET_DDL"):
			return fakeResult{cols: []string{"DDL"}, rows: [][]any{{"\n  CREATE TABLE \"HR\".\"EMP\" (x NUMBER)  "}}}
		}
		return fakeResult{}
	})
	ctx := context.Background()
	b, err := src.ObjectFile(ctx, "sproc", dbfs.Object{Owner: "HR", Name: "P"}, "source.sql", 1<<20)
	if err != nil || string(b) != "CREATE OR REPLACE PROCEDURE p IS\nBEGIN NULL; END;\n/\n" {
		t.Errorf("source.sql = %q %v", b, err)
	}
	b, err = src.ObjectFile(ctx, "table", dbfs.Object{Owner: "HR", Name: "EMP"}, "ddl.sql", 1<<20)
	if err != nil || string(b) != "CREATE TABLE \"HR\".\"EMP\" (x NUMBER);\n" {
		t.Errorf("ddl.sql = %q %v", b, err)
	}

	// DBMS_METADATA failing with ORA-01456 is retried outside READ ONLY, still rolled back.
	fdb.mu.Lock()
	fdb.log = nil
	fdb.respond = func(q string) fakeResult {
		if contains(q, "DBMS_METADATA") {
			last := fdb.log[len(fdb.log)-2]
			if last == "SET TRANSACTION READ ONLY" {
				roAttempts++
				return fakeResult{err: errors.New("ORA-01456: may not perform insert/delete/update operation inside a READ ONLY transaction")}
			}
			rwAttempts++
			return fakeResult{cols: []string{"DDL"}, rows: [][]any{{"CREATE VIEW v AS SELECT 1 FROM dual"}}}
		}
		return fakeResult{}
	}
	fdb.mu.Unlock()
	b, err = src.ObjectFile(ctx, "view", dbfs.Object{Owner: "HR", Name: "V"}, "ddl.sql", 1<<20)
	if err != nil || string(b) != "CREATE VIEW v AS SELECT 1 FROM dual;\n" || roAttempts != 1 || rwAttempts != 1 {
		t.Errorf("ddl retry: %q %v ro=%d rw=%d", b, err, roAttempts, rwAttempts)
	}
	ev := fdb.events()
	if n := strings.Count(strings.Join(ev, "|"), "ROLLBACK"); n != 2 || strings.Contains(strings.Join(ev, "|"), "COMMIT") {
		t.Errorf("events = %q", ev)
	}

	// Non-DDL files are never retried without READ ONLY.
	fdb.mu.Lock()
	fdb.log = nil
	fdb.respond = func(string) fakeResult { return fakeResult{err: errors.New("ORA-01456")} }
	fdb.mu.Unlock()
	if _, err := src.ObjectFile(ctx, "view", dbfs.Object{Owner: "HR", Name: "V"}, "rows.csv", 1<<20); err == nil {
		t.Error("expected error")
	}
	for _, e := range fdb.events() {
		if strings.HasPrefix(e, "SELECT") {
			t.Errorf("statement ran after failed SET TRANSACTION: %s", e)
		}
	}
}

func TestTruncateText(t *testing.T) {
	b := truncateText([]byte(strings.Repeat("x", 200)), 100)
	if len(b) != 100 || !strings.HasSuffix(string(b), "truncated at export.maxFileBytes]\n") {
		t.Errorf("%d %q", len(b), b)
	}
}

func TestKindsMatchDbfsRules(t *testing.T) {
	src, _ := newFakeSource(t, config.Export{MaxRows: 0}, func(string) fakeResult { return fakeResult{} })
	dbfs.New(src, dbfs.Options{}) // panics on an invalid layout
	for _, k := range src.Kinds() {
		for _, f := range k.Files {
			if f == "rows.csv" {
				t.Error("rows.csv present with maxRows = 0")
			}
		}
	}
}
