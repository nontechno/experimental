package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/sijms/go-ora/v2"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "db.schema.check: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath = flag.String("config", "app.conf", "path to the configuration file")
		schema     = flag.String("schema", "", "schema to report on (default: the session's current schema)")
		format     = flag.String("format", "text", "output format: text or json")
		output     = flag.String("out", "-", "output file, or - for stdout")
		timeout    = flag.Duration("timeout", 2*time.Minute, "overall timeout for connecting and querying")
		verbose    = flag.Bool("v", false, "log the resolved connection target to stderr")
	)
	flag.Usage = usage
	flag.Parse()

	if *format != "text" && *format != "json" {
		return fmt.Errorf("unknown -format %q (want text or json)", *format)
	}

	cfg, err := LoadDatabaseConfig(*configPath)
	if err != nil {
		return err
	}

	target, dsn, err := BuildDSN(cfg)
	if err != nil {
		return err
	}
	if *verbose {
		fmt.Fprintf(os.Stderr, "connecting to %s as %s\n", target, cfg.Username)
		fmt.Fprintf(os.Stderr, "dsn: %s\n", RedactDSN(dsn))
	}

	// Ctrl-C cancels in-flight queries rather than leaving them running.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	db, err := sql.Open("oracle", dsn)
	if err != nil {
		return fmt.Errorf("opening connection: %w", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(*timeout)

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connecting to %s as %s: %w", target, cfg.Username, err)
	}

	in := &Inspector{DB: db}

	owner := firstNonEmpty(*schema, cfg.Schema)
	if owner == "" {
		if owner, err = in.CurrentSchema(ctx); err != nil {
			return err
		}
	}
	owner = normalizeIdentifier(owner)

	result, err := in.Inspect(ctx, owner)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("timed out after %s while reading the data dictionary "+
				"(raise -timeout for very large schemas)", *timeout)
		}
		return err
	}
	result.Database.Endpoint = target.String()

	w := os.Stdout
	if *output != "-" {
		f, err := os.Create(*output)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	bw := bufio.NewWriter(w)
	defer bw.Flush()

	if *format == "json" {
		return WriteJSON(bw, result)
	}
	return WriteText(bw, result)
}

// normalizeIdentifier upper-cases a schema name unless it is double-quoted,
// matching how Oracle stores identifiers in the data dictionary.
func normalizeIdentifier(name string) string {
	name = strings.TrimSpace(name)
	if len(name) >= 2 && strings.HasPrefix(name, `"`) && strings.HasSuffix(name, `"`) {
		return name[1 : len(name)-1]
	}
	return strings.ToUpper(name)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func usage() {
	fmt.Fprintf(os.Stderr, `db.schema.check - report the schema of an Oracle database

Usage:
  db.schema.check [flags]

The configuration file is HOCON-flavoured:

  database {
      url                = "jdbc:oracle:thin:@//localhost:1521/xepdb1"
      username           = "scott"
      password           = ${ORACLE_PASSWORD}
      authenticationType = "PASSWORD"
  }

Flags:
`)
	flag.PrintDefaults()
}
