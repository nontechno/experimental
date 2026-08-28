package sink

import (
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nontechno/experimental/otel.sink/internal/model"
)

// buildTraces makes a batch with two resources, two scopes each, so the
// pruning walk has to keep its index aligned across nesting levels.
//
//	resource 0: scope 0 -> a, b   scope 1 -> c
//	resource 1: scope 0 -> d, e   scope 1 -> f
func buildTraces() ptrace.Traces {
	td := ptrace.NewTraces()
	layout := [][]([]string){
		{{"a", "b"}, {"c"}},
		{{"d", "e"}, {"f"}},
	}
	for _, scopes := range layout {
		rs := td.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("service.name", "svc")
		for _, spanNames := range scopes {
			ss := rs.ScopeSpans().AppendEmpty()
			for _, name := range spanNames {
				span := ss.Spans().AppendEmpty()
				span.SetName(name)
				span.Attributes().PutStr("token", "SECRET")
			}
		}
	}
	return td
}

func spanNamesOf(td ptrace.Traces) []string {
	var out []string
	rss := td.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		sss := rss.At(i).ScopeSpans()
		for j := 0; j < sss.Len(); j++ {
			spans := sss.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				out = append(out, spans.At(k).Name())
			}
		}
	}
	return out
}

func keepByName(records []model.Span, names ...string) []model.Span {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var kept []model.Span
	for _, r := range records {
		if want[r.Name] {
			kept = append(kept, r)
		}
	}
	return kept
}

func TestFlattenAssignsSequentialIndexes(t *testing.T) {
	records := model.FlattenTraces(buildTraces(), timeZero())
	if len(records) != 6 {
		t.Fatalf("got %d records, want 6", len(records))
	}
	for i, r := range records {
		if r.Index != i {
			t.Fatalf("record %d has Index %d; pruning relies on these matching traversal order", i, r.Index)
		}
	}
}

func TestPruneRemovesDroppedSpansAndEmptyParents(t *testing.T) {
	td := buildTraces()
	records := model.FlattenTraces(td, timeZero())
	pruneTraces(td, keepByName(records, "a", "b", "f"))

	got := spanNamesOf(td)
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "f" {
		t.Fatalf("got %v, want [a b f]", got)
	}
	if td.SpanCount() != 3 {
		t.Fatalf("span count %d, want 3", td.SpanCount())
	}
	// Resource 0 lost its second scope; resource 1 lost its first.
	if n := td.ResourceSpans().At(0).ScopeSpans().Len(); n != 1 {
		t.Fatalf("resource 0 has %d scopes, want 1 (the emptied scope should be gone)", n)
	}
	if td.ResourceSpans().Len() != 2 {
		t.Fatalf("got %d resources, want 2", td.ResourceSpans().Len())
	}
}

// The important one: a redact filter must not be silently bypassed by
// forwarding the untouched original upstream.
func TestPrunePropagatesAttributeEdits(t *testing.T) {
	td := buildTraces()
	records := model.FlattenTraces(td, timeZero())
	for i := range records {
		records[i].Attributes["token"] = "[REDACTED]"
	}
	pruneTraces(td, records)

	rss := td.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		sss := rss.At(i).ScopeSpans()
		for j := 0; j < sss.Len(); j++ {
			spans := sss.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				v, ok := spans.At(k).Attributes().Get("token")
				if !ok || v.Str() != "[REDACTED]" {
					t.Fatalf("span %s still carries %v: redaction did not reach the forwarded batch",
						spans.At(k).Name(), v.AsString())
				}
			}
		}
	}
}

func TestPruneEverythingLeavesEmptyBatch(t *testing.T) {
	td := buildTraces()
	pruneTraces(td, nil)
	if td.SpanCount() != 0 || td.ResourceSpans().Len() != 0 {
		t.Fatalf("want an empty batch, got %d spans in %d resources",
			td.SpanCount(), td.ResourceSpans().Len())
	}
}

func TestPruneNothingLeavesBatchIntact(t *testing.T) {
	td := buildTraces()
	records := model.FlattenTraces(td, timeZero())
	pruneTraces(td, records)
	if got := spanNamesOf(td); len(got) != 6 {
		t.Fatalf("got %v, want all six spans", got)
	}
}

func TestPruneKeepsOnlyTailSpan(t *testing.T) {
	td := buildTraces()
	records := model.FlattenTraces(td, timeZero())
	pruneTraces(td, records[len(records)-1:])
	got := spanNamesOf(td)
	if len(got) != 1 || got[0] != "f" {
		t.Fatalf("got %v, want [f]", got)
	}
}

func TestPruneKeepsOnlyHeadSpan(t *testing.T) {
	td := buildTraces()
	records := model.FlattenTraces(td, timeZero())
	pruneTraces(td, records[:1])
	got := spanNamesOf(td)
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("got %v, want [a]", got)
	}
}

func TestPruneLogs(t *testing.T) {
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	sl := rl.ScopeLogs().AppendEmpty()
	for _, body := range []string{"keep", "drop", "keep2"} {
		lr := sl.LogRecords().AppendEmpty()
		lr.Body().SetStr(body)
		lr.Attributes().PutStr("token", "SECRET")
	}

	records := model.FlattenLogs(ld, timeZero())
	var kept []model.Log
	for _, r := range records {
		if r.Body != "drop" {
			r.Attributes["token"] = "[REDACTED]"
			kept = append(kept, r)
		}
	}
	pruneLogs(ld, kept)

	if ld.LogRecordCount() != 2 {
		t.Fatalf("log count %d, want 2", ld.LogRecordCount())
	}
	out := ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	if out.At(0).Body().AsString() != "keep" || out.At(1).Body().AsString() != "keep2" {
		t.Fatalf("wrong records survived: %s, %s",
			out.At(0).Body().AsString(), out.At(1).Body().AsString())
	}
	if v, _ := out.At(0).Attributes().Get("token"); v.Str() != "[REDACTED]" {
		t.Fatal("log attribute edit did not propagate")
	}
}

// timeZero keeps these tests independent of the clock: Flatten only stamps
// the received time, which pruning does not look at.
func timeZero() time.Time { return time.Time{} }
