package sink

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nontechno/experimental/otel.sink/internal/model"
)

func TestJSONLIsOneObjectPerLine(t *testing.T) {
	dir := t.TempDir()
	f, err := NewFile(dir, FormatJSONL)
	if err != nil {
		t.Fatal(err)
	}
	f.Spans([]model.Span{{Name: "a"}, {Name: "b"}})
	f.Spans([]model.Span{{Name: "c"}})
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	lines := nonEmptyLines(t, filepath.Join(dir, "traces.jsonl"))
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %q", len(lines), lines)
	}
	for _, line := range lines {
		var span model.Span
		if err := json.Unmarshal([]byte(line), &span); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
	}
}

// The array must stay well-formed across separate batches, which is where
// the comma placement is easy to get wrong.
func TestJSONIsOneArrayAcrossBatches(t *testing.T) {
	dir := t.TempDir()
	f, err := NewFile(dir, FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	f.Spans([]model.Span{{Name: "a"}, {Name: "b"}})
	f.Spans([]model.Span{{Name: "c"}})
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var spans []model.Span
	b, err := os.ReadFile(filepath.Join(dir, "traces.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &spans); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, b)
	}
	if len(spans) != 3 || spans[0].Name != "a" || spans[2].Name != "c" {
		t.Fatalf("got %d spans %v, want [a b c]", len(spans), spans)
	}
}

// A run that captured nothing should still leave a parseable file.
func TestEmptyJSONFileIsValid(t *testing.T) {
	dir := t.TempDir()
	f, err := NewFile(dir, FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"traces.json", "metrics.json", "logs.json"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var out []any
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("%s: %v (%q)", name, err, b)
		}
		if len(out) != 0 {
			t.Fatalf("%s: want empty array, got %v", name, out)
		}
	}
}

func TestAllThreeSignalsGetTheirOwnFile(t *testing.T) {
	dir := t.TempDir()
	f, err := NewFile(dir, FormatJSONL)
	if err != nil {
		t.Fatal(err)
	}
	f.Spans([]model.Span{{Name: "span"}})
	f.Metrics([]model.Metric{{Name: "metric"}})
	f.Logs([]model.Log{{Body: "log"}})
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"traces.jsonl":  "span",
		"metrics.jsonl": "metric",
		"logs.jsonl":    "log",
	} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), want) {
			t.Fatalf("%s should contain %q, got %s", name, want, b)
		}
	}
}

// JSONL appends, so restarting keeps the earlier run's records.
func TestJSONLAppendsAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"first", "second"} {
		f, err := NewFile(dir, FormatJSONL)
		if err != nil {
			t.Fatal(err)
		}
		f.Spans([]model.Span{{Name: name}})
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if lines := nonEmptyLines(t, filepath.Join(dir, "traces.jsonl")); len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
}

func TestRejectsUnknownFormat(t *testing.T) {
	if _, err := NewFile(t.TempDir(), "yaml"); err == nil {
		t.Fatal("want an error for an unsupported format")
	}
}

func nonEmptyLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
