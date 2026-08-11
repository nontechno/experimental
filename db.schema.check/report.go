package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// WriteJSON emits the schema as indented JSON.
func WriteJSON(w io.Writer, s *Schema) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// WriteText emits a human-readable schema report.
func WriteText(w io.Writer, s *Schema) error {
	bw := &errWriter{w: w}

	bw.printf("Schema:   %s\n", s.Owner)
	if s.Database.Endpoint != "" {
		bw.printf("Endpoint: %s\n", s.Database.Endpoint)
	}
	if s.Database.SessionUser != "" {
		bw.printf("User:     %s\n", s.Database.SessionUser)
	}
	if s.Database.Banner != "" {
		bw.printf("Server:   %s\n", s.Database.Banner)
	}
	if s.Database.DatabaseName != "" {
		bw.printf("Database: %s (instance %s)\n", s.Database.DatabaseName, s.Database.InstanceName)
	}
	bw.printf("Objects:  %d tables, %d views, %d sequences\n", len(s.Tables), len(s.Views), len(s.Sequences))

	if len(s.Tables) > 0 {
		bw.printf("\n%s\nTABLES (%d)\n%s\n", strings.Repeat("=", 72), len(s.Tables), strings.Repeat("=", 72))
		for _, t := range s.Tables {
			writeTable(bw, t)
		}
	}

	if len(s.Views) > 0 {
		bw.printf("\n%s\nVIEWS (%d)\n%s\n", strings.Repeat("=", 72), len(s.Views), strings.Repeat("=", 72))
		for _, v := range s.Views {
			writeView(bw, v)
		}
	}

	if len(s.Sequences) > 0 {
		bw.printf("\n%s\nSEQUENCES (%d)\n%s\n\n", strings.Repeat("=", 72), len(s.Sequences), strings.Repeat("=", 72))
		writeTabular(bw, func(tw io.Writer) {
			fmt.Fprintln(tw, "NAME\tINCREMENT\tLAST NUMBER\tCACHE\tCYCLE")
			for _, q := range s.Sequences {
				cycle := "N"
				if q.Cycle {
					cycle = "Y"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", q.Name, q.IncrementBy, q.LastNumber, q.CacheSize, cycle)
			}
		})
	}

	if len(s.Tables) == 0 && len(s.Views) == 0 {
		bw.printf("\nNo tables or views are visible in schema %s.\n"+
			"Check the schema name, and that the connected user has SELECT privileges on it.\n", s.Owner)
	}

	return bw.err
}

func writeTable(w *errWriter, t *Table) {
	title := t.Name
	var tags []string
	if t.Temporary {
		tags = append(tags, "temporary")
	}
	if t.Partitioned {
		tags = append(tags, "partitioned")
	}
	if t.NumRows != nil {
		tags = append(tags, fmt.Sprintf("~%d rows", *t.NumRows))
	}
	if len(tags) > 0 {
		title += "  [" + strings.Join(tags, ", ") + "]"
	}
	w.printf("\n%s\n%s\n", title, strings.Repeat("-", len(title)))
	if t.Comment != "" {
		w.printf("  %s\n", t.Comment)
	}

	writeColumns(w, t.Columns)

	for _, c := range t.Constraints {
		w.printf("  %s\n", formatConstraint(c))
	}
	for _, idx := range t.Indexes {
		kind := "IX  "
		if idx.Unique {
			kind = "IXU "
		}
		line := fmt.Sprintf("  %s %s (%s)", kind, idx.Name, strings.Join(idx.Columns, ", "))
		if idx.Type != "" && idx.Type != "NORMAL" {
			line += " [" + idx.Type + "]"
		}
		if idx.Status != "" && idx.Status != "VALID" && idx.Status != "N/A" {
			line += " [" + idx.Status + "]"
		}
		w.printf("%s\n", line)
	}
}

func writeView(w *errWriter, v *View) {
	w.printf("\n%s\n%s\n", v.Name, strings.Repeat("-", len(v.Name)))
	if v.Comment != "" {
		w.printf("  %s\n", v.Comment)
	}
	writeColumns(w, v.Columns)
}

func writeColumns(w *errWriter, cols []*Column) {
	if len(cols) == 0 {
		w.printf("  (no columns visible)\n")
		return
	}
	writeTabular(w, func(tw io.Writer) {
		fmt.Fprintln(tw, "  #\tCOLUMN\tTYPE\tNULL\tDEFAULT")
		for _, c := range cols {
			null := "NOT NULL"
			if c.Nullable {
				null = ""
			}
			def := truncate(singleLine(c.Default), 40)
			if c.Identity {
				def = "IDENTITY"
			}
			fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\n", c.Position, c.Name, c.Type, null, def)
		}
	})

	for _, c := range cols {
		if c.Comment != "" {
			w.printf("    -- %s: %s\n", c.Name, truncate(singleLine(c.Comment), 100))
		}
	}
}

func formatConstraint(c *Constraint) string {
	var b strings.Builder
	switch c.Type {
	case "PRIMARY KEY":
		b.WriteString("PK   ")
	case "UNIQUE":
		b.WriteString("UQ   ")
	case "FOREIGN KEY":
		b.WriteString("FK   ")
	default:
		b.WriteString("CK   ")
	}
	b.WriteString(c.Name)
	if len(c.Columns) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(c.Columns, ", "))
	}
	if c.Type == "FOREIGN KEY" && c.RefTable != "" {
		fmt.Fprintf(&b, " -> %s(%s)", c.RefTable, strings.Join(c.RefColumns, ", "))
		if c.DeleteRule != "" && c.DeleteRule != "NO ACTION" {
			fmt.Fprintf(&b, " ON DELETE %s", c.DeleteRule)
		}
	}
	if c.Type == "CHECK" && c.Condition != "" {
		fmt.Fprintf(&b, " CHECK (%s)", truncate(singleLine(c.Condition), 80))
	}
	if c.Status != "" && c.Status != "ENABLED" {
		fmt.Fprintf(&b, " [%s]", c.Status)
	}
	return b.String()
}

// writeTabular renders tab-separated rows into aligned columns, dropping the
// trailing padding tabwriter leaves behind when the last cells are empty.
func writeTabular(w io.Writer, emit func(io.Writer)) {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	emit(tw)
	tw.Flush()

	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
}

func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// errWriter latches the first write error so callers can check once.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return len(p), nil
	}
	n, err := e.w.Write(p)
	e.err = err
	return n, err
}

func (e *errWriter) printf(format string, args ...any) {
	fmt.Fprintf(e, format, args...)
}
