package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	_ "github.com/sijms/go-ora/v2"
)

const timeFormat = time.RFC3339Nano

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		cfgPath   = flag.String("config", "application.conf", "path to the HOCON configuration file")
		format    = flag.String("format", "json", "output format: json or csv")
		delimiter = flag.String("delimiter", ",", "field delimiter for csv output")
		noHeader  = flag.Bool("no-header", false, "omit the header row in csv output")
		nullText  = flag.String("null", "", "text used for NULL values in csv output")
		limit     = flag.Int("limit", 0, "maximum number of rows to fetch (0 = no limit)")
		where     = flag.String("where", "", "optional WHERE clause, inserted verbatim (without the WHERE keyword)")
		outPath   = flag.String("out", "-", "output file, or - for stdout")
		timeout   = flag.Duration("timeout", 5*time.Minute, "overall time budget for connecting and reading")
		printDSN  = flag.Bool("print-dsn", false, "print the derived connection string (password redacted) and exit")
	)

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"Usage: %s [options] <table>\n\nDumps an Oracle table as pretty JSON or delimited text.\n\nOptions:\n",
			os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		return err
	}

	target, err := ParseJDBCURL(cfg.Database.URL)
	if err != nil {
		return err
	}

	if *printDSN {
		fmt.Println(target.Redacted(cfg.Database.Username))
		return nil
	}

	if flag.NArg() != 1 {
		flag.Usage()
		return errors.New("exactly one table name is required")
	}
	table, err := validateIdentifier(flag.Arg(0))
	if err != nil {
		return err
	}

	var writer RowWriter
	out := io.Writer(os.Stdout)
	if *outPath != "-" {
		f, err := os.Create(*outPath)
		if err != nil {
			return fmt.Errorf("creating output file: %w", err)
		}
		defer f.Close()
		out = f
	}

	switch strings.ToLower(*format) {
	case "json":
		writer = NewJSONWriter(out)
	case "csv", "delimited":
		d := []rune(*delimiter)
		if len(d) != 1 {
			return fmt.Errorf("delimiter must be exactly one character, got %q", *delimiter)
		}
		writer = NewCSVWriter(out, d[0], !*noHeader, *nullText)
	default:
		return fmt.Errorf("unknown format %q (want json or csv)", *format)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, *timeout)
	defer cancelTimeout()

	dsn := target.BuildDSN(cfg.Database.Username, cfg.Database.Password)
	db, err := sql.Open("oracle", dsn)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connecting to %s: %w", target.Redacted(cfg.Database.Username), err)
	}

	return dumpTable(ctx, db, writer, table, *where, *limit)
}

func dumpTable(ctx context.Context, db *sql.DB, w RowWriter, table, where string, limit int) error {
	query := "SELECT * FROM " + table
	if strings.TrimSpace(where) != "" {
		query += " WHERE " + where
	}
	if limit > 0 {
		// Requires Oracle 12c or newer.
		query += fmt.Sprintf(" FETCH FIRST %d ROWS ONLY", limit)
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("querying %s: %w", table, err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("reading column names: %w", err)
	}
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return fmt.Errorf("reading column types: %w", err)
	}

	dests := make([]any, len(columns))
	for i := range columns {
		dests[i] = scanDest(colTypes[i])
	}

	if err := w.Begin(columns); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}

	values := make([]any, len(columns))
	for rows.Next() {
		if err := rows.Scan(dests...); err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}
		for i := range dests {
			v, err := normalize(dests[i], timeFormat)
			if err != nil {
				return fmt.Errorf("converting column %s: %w", columns[i], err)
			}
			values[i] = v
		}
		if err := w.Write(values); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading rows: %w", err)
	}

	if err := w.End(); err != nil {
		return fmt.Errorf("flushing output: %w", err)
	}
	return nil
}

var unquotedIdent = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_$#]*$`)

// validateIdentifier accepts SCHEMA.TABLE, TABLE, and the quoted forms of
// either, rejecting anything else. The table name is concatenated into the
// SQL text (bind variables cannot name objects), so it must be checked.
func validateIdentifier(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("empty table name")
	}

	parts := splitIdentifier(name)
	if len(parts) == 0 || len(parts) > 2 {
		return "", fmt.Errorf("invalid table name %q (want TABLE or SCHEMA.TABLE)", name)
	}
	for _, p := range parts {
		if strings.HasPrefix(p, `"`) {
			if len(p) < 3 || !strings.HasSuffix(p, `"`) || strings.Contains(p[1:len(p)-1], `"`) {
				return "", fmt.Errorf("invalid quoted identifier %q", p)
			}
			continue
		}
		if !unquotedIdent.MatchString(p) || len(p) > 128 {
			return "", fmt.Errorf("invalid identifier %q", p)
		}
	}
	return name, nil
}

// splitIdentifier splits on '.' while leaving dots inside quotes alone.
func splitIdentifier(name string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range name {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == '.' && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote {
		return nil
	}
	parts = append(parts, cur.String())
	return parts
}
