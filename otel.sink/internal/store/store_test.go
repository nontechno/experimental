package store

import (
	"sync"
	"testing"

	"github.com/nontechno/experimental/otel.sink/internal/model"
)

func TestRingOverwritesOldest(t *testing.T) {
	s := New(3, 3, 3)
	for i := 0; i < 5; i++ {
		s.Spans([]model.Span{{Name: string(rune('a' + i)), Service: "svc"}})
	}
	got := s.QuerySpans(SpanQuery{Limit: 10})
	if len(got) != 3 {
		t.Fatalf("held %d spans, want 3", len(got))
	}
	// Newest first.
	if got[0].Name != "e" || got[2].Name != "c" {
		t.Fatalf("got %s..%s, want e..c", got[0].Name, got[2].Name)
	}
	if st := s.Stats(); st.SpansReceived != 5 {
		t.Fatalf("counter = %d, want 5", st.SpansReceived)
	}
}

func TestRingBatchLargerThanCapacity(t *testing.T) {
	s := New(2, 2, 2)
	s.Spans([]model.Span{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}})
	got := s.QuerySpans(SpanQuery{Limit: 10})
	if len(got) != 2 || got[0].Name != "d" || got[1].Name != "c" {
		t.Fatalf("got %+v, want [d c]", names(got))
	}
}

func TestSpanFilters(t *testing.T) {
	s := New(10, 10, 10)
	s.Spans([]model.Span{
		{Name: "GET /health", Service: "api", TraceID: "aa", StatusCode: "Unset"},
		{Name: "SELECT users", Service: "db", TraceID: "bb", StatusCode: "Error"},
		{Name: "GET /orders", Service: "api", TraceID: "aa", StatusCode: "Error"},
	})

	if got := s.QuerySpans(SpanQuery{Service: "API"}); len(got) != 2 {
		t.Fatalf("service filter returned %d, want 2 (match must be case-insensitive)", len(got))
	}
	if got := s.QuerySpans(SpanQuery{Name: "get "}); len(got) != 2 {
		t.Fatalf("name substring returned %d, want 2", len(got))
	}
	if got := s.QuerySpans(SpanQuery{Status: "Error"}); len(got) != 2 {
		t.Fatalf("status filter returned %d, want 2", len(got))
	}
	if got := s.Trace("AA"); len(got) != 2 {
		t.Fatalf("trace lookup returned %d, want 2", len(got))
	}
	if got := s.QuerySpans(SpanQuery{Limit: 1}); len(got) != 1 {
		t.Fatalf("limit returned %d, want 1", len(got))
	}
}

func TestLogMinSeverity(t *testing.T) {
	s := New(10, 10, 10)
	s.Logs([]model.Log{
		{Body: "starting", SeverityNo: 9, Service: "api"},
		{Body: "disk almost full", SeverityNo: 13, Service: "api"},
		{Body: "connection refused", SeverityNo: 17, Service: "api"},
	})
	if got := s.QueryLogs(LogQuery{MinSeverity: 13}); len(got) != 2 {
		t.Fatalf("min severity returned %d, want 2", len(got))
	}
	if got := s.QueryLogs(LogQuery{Contains: "REFUSED"}); len(got) != 1 {
		t.Fatalf("body substring returned %d, want 1", len(got))
	}
}

func TestConcurrentWrites(t *testing.T) {
	s := New(100, 100, 100)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Spans([]model.Span{{Name: "x"}})
			s.Logs([]model.Log{{Body: "y"}})
			_ = s.Stats()
		}()
	}
	wg.Wait()
	if st := s.Stats(); st.SpansReceived != 50 || st.LogsReceived != 50 {
		t.Fatalf("counters = %d/%d, want 50/50", st.SpansReceived, st.LogsReceived)
	}
}

func names(spans []model.Span) []string {
	out := make([]string, len(spans))
	for i := range spans {
		out[i] = spans[i].Name
	}
	return out
}
