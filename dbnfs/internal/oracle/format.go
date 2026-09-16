package oracle

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"strings"
)

// querier is satisfied by *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// tsvEscape makes a value safe for one TSV cell.
var tsvEscaper = strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`)

// rowSink receives rows of nullable strings; it returns false to stop.
type rowSink func(cols []string, vals []sql.NullString) (more bool, err error)

// scanStrings runs query and feeds every row to sink as strings. The query
// is expected to convert non-character columns to text itself.
func scanStrings(ctx context.Context, q querier, header func(cols []string), sink rowSink, query string, args ...any) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	if header != nil {
		header(cols)
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		more, err := sink(cols, vals)
		if err != nil {
			return err
		}
		if !more {
			return nil // rows.Close cancels the cursor
		}
	}
	return rows.Err()
}

// queryTSV renders a query as TSV with a header row. NULL is an empty cell;
// tab, newline, CR and backslash are backslash-escaped. Output stops at a
// row boundary once limit bytes or maxRows rows would be exceeded.
func queryTSV(ctx context.Context, q querier, limit, maxRows int, query string, args ...any) ([]byte, error) {
	var buf bytes.Buffer
	n := 0
	header := func(cols []string) {
		buf.WriteString(strings.ToLower(strings.Join(cols, "\t")))
		buf.WriteByte('\n')
	}
	err := scanStrings(ctx, q, header, func(_ []string, vals []sql.NullString) (bool, error) {
		mark := buf.Len()
		for i, v := range vals {
			if i > 0 {
				buf.WriteByte('\t')
			}
			if v.Valid {
				buf.WriteString(tsvEscaper.Replace(v.String))
			}
		}
		buf.WriteByte('\n')
		if buf.Len() > limit {
			buf.Truncate(mark)
			return false, nil
		}
		n++
		return n < maxRows, nil
	}, query, args...)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// errNoRows means the object is not visible (dropped, or no privilege).
var errNoRows = errors.New("no rows returned: the object was dropped or is not visible to this user")

// queryKV renders the first row of a query as "column: value" lines.
func queryKV(ctx context.Context, q querier, query string, args ...any) ([]byte, error) {
	var buf bytes.Buffer
	found := false
	err := scanStrings(ctx, q, nil, func(cols []string, vals []sql.NullString) (bool, error) {
		found = true
		width := 0
		for _, c := range cols {
			width = max(width, len(c))
		}
		for i, c := range cols {
			fmt.Fprintf(&buf, "%-*s  %s\n", width+1, strings.ToLower(c)+":", tsvEscaper.Replace(vals[i].String))
		}
		return false, nil
	}, query, args...)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errNoRows
	}
	return buf.Bytes(), nil
}

// csvBuffer writes RFC 4180 CSV and enforces a byte limit at row boundaries.
type csvBuffer struct {
	buf   bytes.Buffer
	w     *csv.Writer
	limit int
}

func newCSVBuffer(limit int) *csvBuffer {
	c := &csvBuffer{limit: limit}
	c.w = csv.NewWriter(&c.buf)
	return c
}

// write appends a record; it returns false (and discards the record) if
// the record would exceed the limit.
func (c *csvBuffer) write(record []string) (bool, error) {
	mark := c.buf.Len()
	if err := c.w.Write(record); err != nil {
		return false, err
	}
	c.w.Flush()
	if err := c.w.Error(); err != nil {
		return false, err
	}
	if c.buf.Len() > c.limit {
		c.buf.Truncate(mark)
		return false, nil
	}
	return true, nil
}
