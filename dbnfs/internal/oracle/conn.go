// Package oracle implements dbfs.Source for Oracle Database using the
// pure-Go go-ora driver (no Instant Client, no CGO).
package oracle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "github.com/sijms/go-ora/v2" // registers the "oracle" driver

	"dbnfs/internal/config"
)

// connectionURL builds a go-ora URL. url.URL escapes the user info
// correctly for any password; go-ora's BuildUrl uses PathEscape, which
// leaves '@' and ':' unescaped.
func connectionURL(d config.Database) string {
	u := url.URL{
		Scheme: "oracle",
		User:   url.UserPassword(d.Username, d.Password),
		Host:   net.JoinHostPort(d.Target.Host, strconv.Itoa(d.Target.Port)),
		Path:   "/" + d.Target.Service,
	}
	q := url.Values{}
	q.Set("CONNECTION TIMEOUT", "15")
	q.Set("PROGRAM", "dbnfs")
	if d.Target.SID != "" {
		u.Path = "/"
		q.Set("SID", d.Target.SID)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Open connects and verifies the session.
func Open(ctx context.Context, d config.Database) (*sql.DB, error) {
	if strings.EqualFold(d.Username, "SYS") {
		// go-ora silently upgrades SYS to a SYSDBA session.
		return nil, errors.New("refusing to connect as SYS; use a dedicated read-only account")
	}
	db, err := sql.Open("oracle", connectionURL(d))
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", d.Target, err)
	}
	db.SetMaxOpenConns(d.MaxConnections)
	db.SetMaxIdleConns(d.MaxConnections)
	db.SetConnMaxIdleTime(5 * time.Minute) // firewalls often drop idle sessions
	db.SetConnMaxLifetime(time.Hour)

	pingCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connecting to %s as %s: %w", d.Target, d.Username, err)
	}
	return db, nil
}

// quoteIdent quotes an identifier taken from the data dictionary for use in
// generated SQL. Oracle identifiers cannot contain a double quote or NUL, so
// anything that does is rejected rather than escaped.
func quoteIdent(s string) (string, error) {
	if s == "" || len(s) > 128*4 || strings.ContainsAny(s, "\"\x00") {
		return "", fmt.Errorf("refusing to quote identifier %q", s)
	}
	return `"` + s + `"`, nil
}

// inTx runs fn in a transaction that is always rolled back. With readOnly,
// the transaction starts with SET TRANSACTION READ ONLY, so any attempt to
// modify data (for example from a function called by a view) fails with
// ORA-01456 instead of taking effect.
func inTx(ctx context.Context, db *sql.DB, readOnly bool, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // nothing to keep; a failed rollback discards the session
	if readOnly {
		if _, err := tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
			return fmt.Errorf("SET TRANSACTION READ ONLY: %w", err)
		}
	}
	return fn(tx)
}

// isReadOnlyViolation reports errors raised when a read-only transaction
// attempts a write.
func isReadOnlyViolation(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "ORA-01456") || strings.Contains(err.Error(), "ORA-16000"))
}
