package sink

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/nontechno/experimental/otel.sink/internal/model"
)

// Output formats for the file sink.
const (
	// FormatJSONL writes one JSON object per line. Records are appended, so
	// the file stays valid after a crash and can be tailed while it grows.
	FormatJSONL = "jsonl"
	// FormatJSON writes a single JSON array, closed on shutdown. Easier to
	// hand to a tool that wants one document, but the file is only complete
	// after a clean exit.
	FormatJSON = "json"
)

// File writes every received record to traces, metrics and logs files in a
// directory, in either format above. Files are not rotated: point Dir at a
// fresh directory per run, or hand the JSONL output to logrotate.
type File struct {
	traces  *jsonFile
	metrics *jsonFile
	logs    *jsonFile
}

// NewFile creates dir if needed and opens the three output files.
func NewFile(dir, format string) (*File, error) {
	switch format {
	case FormatJSON, FormatJSONL:
	default:
		return nil, fmt.Errorf("file format %q: want %s or %s", format, FormatJSONL, FormatJSON)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}

	f := &File{}
	var err error
	for _, spec := range []struct {
		kind string
		dst  **jsonFile
	}{
		{"traces", &f.traces},
		{"metrics", &f.metrics},
		{"logs", &f.logs},
	} {
		*spec.dst, err = openJSONFile(filepath.Join(dir, spec.kind+"."+format), format, spec.kind)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return f, nil
}

// Name implements Sink.
func (f *File) Name() string { return "file" }

// Paths lists the files being written, for logging.
func (f *File) Paths() []string {
	return []string{f.traces.path(), f.metrics.path(), f.logs.path()}
}

// Spans implements Sink.
func (f *File) Spans(spans []model.Span) { writeRecords(f.traces, spans) }

// Metrics implements Sink.
func (f *File) Metrics(metrics []model.Metric) { writeRecords(f.metrics, metrics) }

// Logs implements Sink.
func (f *File) Logs(logs []model.Log) { writeRecords(f.logs, logs) }

// Close finishes each file. For FormatJSON this writes the closing bracket,
// so it must run for the output to be valid JSON.
func (f *File) Close() error {
	var firstErr error
	for _, w := range []*jsonFile{f.traces, f.metrics, f.logs} {
		if err := w.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// jsonFile is one output file in one format.
type jsonFile struct {
	mu     sync.Mutex
	f      *os.File
	format string
	kind   string
	wrote  bool // at least one record written, so the next needs a comma
	closed bool
}

func openJSONFile(path, format, kind string) (*jsonFile, error) {
	flags := os.O_CREATE | os.O_WRONLY
	if format == FormatJSON {
		// A closed JSON array cannot be appended to, so start fresh.
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_APPEND
	}
	h, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if format == FormatJSON {
		if _, err := h.WriteString("[\n"); err != nil {
			_ = h.Close()
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
	}
	return &jsonFile{f: h, format: format, kind: kind}, nil
}

func (w *jsonFile) path() string {
	if w == nil || w.f == nil {
		return ""
	}
	return w.f.Name()
}

// Close writes the array terminator when needed and closes the handle.
func (w *jsonFile) Close() error {
	if w == nil || w.f == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.format == FormatJSON {
		tail := "]\n"
		if w.wrote {
			tail = "\n]\n"
		}
		if _, err := w.f.WriteString(tail); err != nil {
			_ = w.f.Close()
			return err
		}
	}
	return w.f.Close()
}

// writeRecords marshals each record individually and appends only the ones
// that encoded cleanly, so a single bad record is skipped rather than
// truncating a line or dropping the rest of the batch. The whole batch is
// written under one lock, which keeps records from interleaving.
func writeRecords[T any](w *jsonFile, records []T) {
	if w == nil || len(records) == 0 {
		return
	}
	var batch, one bytes.Buffer
	enc := json.NewEncoder(&one)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}

	pending := w.wrote
	for i := range records {
		one.Reset()
		if err := enc.Encode(records[i]); err != nil {
			fmt.Fprintf(os.Stderr, "otel.sink: encode %s: %v\n", w.kind, err)
			continue
		}
		if w.format == FormatJSON {
			if pending {
				batch.WriteString(",\n")
			}
			// Encode appends a newline; inside an array the separator does it.
			batch.Write(bytes.TrimRight(one.Bytes(), "\n"))
		} else {
			batch.Write(one.Bytes())
		}
		pending = true
	}
	if batch.Len() == 0 {
		return
	}
	if _, err := w.f.Write(batch.Bytes()); err != nil {
		fmt.Fprintf(os.Stderr, "otel.sink: write %s: %v\n", w.kind, err)
		return
	}
	w.wrote = pending
}
