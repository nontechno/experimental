package filter

import (
	"fmt"
	"testing"

	"github.com/nontechno/experimental/otel.sink/internal/model"
)

func TestChainAppliesInOrder(t *testing.T) {
	chain, err := Build([]Spec{
		{Name: "drop_names", Options: map[string]string{"contains": "/health"}},
		{Name: "min_duration", Options: map[string]string{"ms": "10"}},
		{Name: "redact", Options: map[string]string{"keys": "db.statement"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := chain.Spans([]model.Span{
		{Name: "GET /health", DurationMS: 100},
		{Name: "GET /orders", DurationMS: 50, Attributes: map[string]any{"db.statement": "SELECT 1"}},
		{Name: "fast.call", DurationMS: 2},
		{Name: "db.query", DurationMS: 30},
	})
	if len(out) != 2 || out[0].Name != "GET /orders" || out[1].Name != "db.query" {
		t.Fatalf("got %v, want [GET /orders db.query]", spanNames(out))
	}
	if out[0].Attributes["db.statement"] != "[REDACTED]" {
		t.Fatalf("attribute not redacted: %v", out[0].Attributes)
	}
}

// A filter that defines only Span must leave the other signals alone.
func TestUnsetSignalsPassThrough(t *testing.T) {
	chain, err := Build([]Spec{{Name: "min_duration", Options: map[string]string{"ms": "1000"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := chain.Logs([]model.Log{{Body: "hello"}}); len(got) != 1 {
		t.Fatalf("logs should pass through, got %d", len(got))
	}
	if got := chain.Metrics([]model.Metric{{Name: "m"}}); len(got) != 1 {
		t.Fatalf("metrics should pass through, got %d", len(got))
	}
	if got := chain.Spans([]model.Span{{DurationMS: 1}}); len(got) != 0 {
		t.Fatalf("span should have been dropped, got %d", len(got))
	}
}

// Sampling must be a per-trace decision, or waterfalls come out with holes.
func TestSampleKeepsWholeTraces(t *testing.T) {
	chain, err := Build([]Spec{{Name: "sample", Options: map[string]string{"ratio": "0.5"}}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("%032x", i*2654435761)
		root := chain.Spans([]model.Span{{TraceID: id, Name: "root"}})
		child := chain.Spans([]model.Span{{TraceID: id, Name: "child"}})
		if len(root) != len(child) {
			t.Fatalf("trace %s was split: root kept=%d child kept=%d", id, len(root), len(child))
		}
	}
}

func TestSampleRatioIsRoughlyAccurate(t *testing.T) {
	chain, err := Build([]Spec{{Name: "sample", Options: map[string]string{"ratio": "0.25"}}})
	if err != nil {
		t.Fatal(err)
	}
	const n = 20000
	kept := 0
	for i := 0; i < n; i++ {
		kept += len(chain.Spans([]model.Span{{TraceID: fmt.Sprintf("trace-%d", i)}}))
	}
	if got := float64(kept) / n; got < 0.22 || got > 0.28 {
		t.Fatalf("kept %.3f, want about 0.25 (hash distribution may be skewed)", got)
	}
}

func TestSampleEdgeRatios(t *testing.T) {
	all, _ := Build([]Spec{{Name: "sample", Options: map[string]string{"ratio": "1"}}})
	none, _ := Build([]Spec{{Name: "sample", Options: map[string]string{"ratio": "0"}}})
	spans := []model.Span{{TraceID: "a"}, {TraceID: "b"}}
	if got := len(all.Spans(append([]model.Span{}, spans...))); got != 2 {
		t.Fatalf("ratio=1 kept %d, want 2", got)
	}
	if got := len(none.Spans(append([]model.Span{}, spans...))); got != 0 {
		t.Fatalf("ratio=0 kept %d, want 0", got)
	}
	if _, err := Build([]Spec{{Name: "sample", Options: map[string]string{"ratio": "2"}}}); err == nil {
		t.Fatal("ratio above 1 should be rejected")
	}
}

func TestSeverityFilters(t *testing.T) {
	chain, err := Build([]Spec{{Name: "min_severity", Options: map[string]string{"severity": "warn"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := chain.Logs([]model.Log{{SeverityNo: 9}, {SeverityNo: 13}, {SeverityNo: 17}})
	if len(got) != 2 {
		t.Fatalf("kept %d, want 2", len(got))
	}
	if _, err := Build([]Spec{{Name: "min_severity", Options: map[string]string{"severity": "loud"}}}); err == nil {
		t.Fatal("an unknown severity name should be rejected")
	}
}

func TestServicesAllowAndDeny(t *testing.T) {
	allow, err := Build([]Spec{{Name: "services", Options: map[string]string{"allow": "checkout"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := allow.Spans([]model.Span{{Service: "checkout"}, {Service: "batch"}}); len(got) != 1 {
		t.Fatalf("allow kept %d, want 1", len(got))
	}
	deny, _ := Build([]Spec{{Name: "services", Options: map[string]string{"deny": "batch"}}})
	if got := deny.Logs([]model.Log{{Service: "checkout"}, {Service: "batch"}}); len(got) != 1 {
		t.Fatalf("deny kept %d, want 1", len(got))
	}
	if _, err := Build([]Spec{{Name: "services", Options: map[string]string{"allow": "a", "deny": "b"}}}); err == nil {
		t.Fatal("allow and deny together should be rejected")
	}
}

func TestErrorsOnly(t *testing.T) {
	chain, err := Build([]Spec{{Name: "errors_only"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := chain.Spans([]model.Span{{StatusCode: "Error"}, {StatusCode: "Unset"}}); len(got) != 1 {
		t.Fatalf("spans kept %d, want 1", len(got))
	}
	if got := chain.Logs([]model.Log{{SeverityNo: 17}, {SeverityNo: 9}}); len(got) != 1 {
		t.Fatalf("logs kept %d, want 1", len(got))
	}
	// Metrics are not part of this filter and must survive.
	if got := chain.Metrics([]model.Metric{{Name: "m"}}); len(got) != 1 {
		t.Fatalf("metrics kept %d, want 1", len(got))
	}
}

func TestRedactReachesEventAndDataPointAttributes(t *testing.T) {
	chain, err := Build([]Spec{{Name: "redact", Options: map[string]string{"keys": "token", "value": "xxx"}}})
	if err != nil {
		t.Fatal(err)
	}
	spans := chain.Spans([]model.Span{{
		Attributes: map[string]any{"token": "abc", "keep": "yes"},
		Events:     []model.SpanEvent{{Attributes: map[string]any{"token": "def"}}},
	}})
	if spans[0].Attributes["token"] != "xxx" || spans[0].Attributes["keep"] != "yes" {
		t.Fatalf("span attributes: %v", spans[0].Attributes)
	}
	if spans[0].Events[0].Attributes["token"] != "xxx" {
		t.Fatalf("event attributes: %v", spans[0].Events[0].Attributes)
	}
	metrics := chain.Metrics([]model.Metric{{
		DataPoints: []model.DataPoint{{Attributes: map[string]any{"token": "ghi"}}},
	}})
	if metrics[0].DataPoints[0].Attributes["token"] != "xxx" {
		t.Fatalf("data point attributes: %v", metrics[0].DataPoints[0].Attributes)
	}
}

func TestBuildRejectsUnknownFilter(t *testing.T) {
	if _, err := Build([]Spec{{Name: "does-not-exist"}}); err == nil {
		t.Fatal("want an error naming the registered filters")
	}
}

func TestFiltersRequiringOptions(t *testing.T) {
	for _, name := range []string{"redact", "drop_names", "services"} {
		if _, err := Build([]Spec{{Name: name}}); err == nil {
			t.Fatalf("%s should require options", name)
		}
	}
}

func TestFuncsNilPassesThrough(t *testing.T) {
	f := Funcs{FilterName: "noop"}
	in := []model.Span{{Name: "a"}, {Name: "b"}}
	if got := f.Spans(in); len(got) != 2 {
		t.Fatalf("kept %d, want 2", len(got))
	}
	if f.Name() != "noop" {
		t.Fatalf("name %q", f.Name())
	}
}

func TestRegisteredIncludesBuiltins(t *testing.T) {
	want := map[string]bool{
		"redact": false, "min_duration": false, "min_severity": false,
		"drop_names": false, "services": false, "sample": false, "errors_only": false,
	}
	for _, name := range Registered() {
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("built-in %q is not registered", name)
		}
	}
}

func spanNames(spans []model.Span) []string {
	out := make([]string, len(spans))
	for i := range spans {
		out[i] = spans[i].Name
	}
	return out
}
