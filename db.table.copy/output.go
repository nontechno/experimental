package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
)

// RowWriter renders a result set incrementally so that large tables never
// have to be held in memory.
type RowWriter interface {
	Begin(columns []string) error
	Write(values []any) error
	End() error
}

// --- JSON ---------------------------------------------------------------

// jsonWriter emits a pretty-printed array of objects. Objects are built by
// hand rather than via a map so that column order is preserved.
type jsonWriter struct {
	w       *bufio.Writer
	cols    []string
	written bool
}

// NewJSONWriter returns a streaming pretty JSON writer.
func NewJSONWriter(w io.Writer) RowWriter {
	return &jsonWriter{w: bufio.NewWriter(w)}
}

func (j *jsonWriter) Begin(columns []string) error {
	j.cols = columns
	_, err := j.w.WriteString("[")
	return err
}

func (j *jsonWriter) Write(values []any) error {
	if len(values) != len(j.cols) {
		return fmt.Errorf("row has %d values but %d columns", len(values), len(j.cols))
	}

	if j.written {
		if _, err := j.w.WriteString(",\n  {\n"); err != nil {
			return err
		}
	} else {
		if _, err := j.w.WriteString("\n  {\n"); err != nil {
			return err
		}
		j.written = true
	}

	for i, name := range j.cols {
		key, err := json.Marshal(name)
		if err != nil {
			return err
		}
		val, err := json.Marshal(values[i])
		if err != nil {
			return fmt.Errorf("encoding column %s: %w", name, err)
		}
		sep := ",\n"
		if i == len(j.cols)-1 {
			sep = "\n"
		}
		if _, err := fmt.Fprintf(j.w, "    %s: %s%s", key, val, sep); err != nil {
			return err
		}
	}

	_, err := j.w.WriteString("  }")
	return err
}

func (j *jsonWriter) End() error {
	if j.written {
		if _, err := j.w.WriteString("\n]\n"); err != nil {
			return err
		}
	} else {
		if _, err := j.w.WriteString("]\n"); err != nil {
			return err
		}
	}
	return j.w.Flush()
}

// --- Delimited ----------------------------------------------------------

type csvWriter struct {
	w        *csv.Writer
	nullText string
	header   bool
	buf      []string
}

// NewCSVWriter returns a streaming delimited writer. delimiter must be a
// single rune; header controls whether a column-name row is emitted.
func NewCSVWriter(w io.Writer, delimiter rune, header bool, nullText string) RowWriter {
	cw := csv.NewWriter(w)
	cw.Comma = delimiter
	return &csvWriter{w: cw, nullText: nullText, header: header}
}

func (c *csvWriter) Begin(columns []string) error {
	c.buf = make([]string, len(columns))
	if !c.header {
		return nil
	}
	return c.w.Write(columns)
}

func (c *csvWriter) Write(values []any) error {
	if len(values) != len(c.buf) {
		return fmt.Errorf("row has %d values but %d columns", len(values), len(c.buf))
	}
	for i, v := range values {
		c.buf[i] = renderText(v, c.nullText)
	}
	return c.w.Write(c.buf)
}

func (c *csvWriter) End() error {
	c.w.Flush()
	return c.w.Error()
}
