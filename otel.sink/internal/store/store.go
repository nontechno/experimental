// Package store keeps the most recent telemetry in memory so the query API
// can serve it. Each signal has its own fixed-capacity ring buffer, so memory
// use is bounded no matter how long the process runs.
package store

import (
	"strings"
	"sync"

	"github.com/nontechno/experimental/otel.sink/internal/model"
)

// Store implements sink.Sink and answers queries.
type Store struct {
	spans   *ring[model.Span]
	metrics *ring[model.Metric]
	logs    *ring[model.Log]
}

// New returns a store keeping at most the given number of records per signal.
// Non-positive capacities fall back to 1000.
func New(maxSpans, maxMetrics, maxLogs int) *Store {
	return &Store{
		spans:   newRing[model.Span](maxSpans),
		metrics: newRing[model.Metric](maxMetrics),
		logs:    newRing[model.Log](maxLogs),
	}
}

// Name implements sink.Sink.
func (s *Store) Name() string { return "store" }

// Close implements sink.Sink. The store holds no resources.
func (s *Store) Close() error { return nil }

// Spans implements sink.Sink.
func (s *Store) Spans(v []model.Span) { s.spans.add(v) }

// Metrics implements sink.Sink.
func (s *Store) Metrics(v []model.Metric) { s.metrics.add(v) }

// Logs implements sink.Sink.
func (s *Store) Logs(v []model.Log) { s.logs.add(v) }

// Stats summarises what the store has seen.
type Stats struct {
	SpansReceived   uint64   `json:"spans_received"`
	MetricsReceived uint64   `json:"metrics_received"`
	LogsReceived    uint64   `json:"logs_received"`
	SpansHeld       int      `json:"spans_held"`
	MetricsHeld     int      `json:"metrics_held"`
	LogsHeld        int      `json:"logs_held"`
	Services        []string `json:"services"`
}

// Stats returns counters plus the distinct service names currently held.
func (s *Store) Stats() Stats {
	spans := s.spans.snapshot()
	metrics := s.metrics.snapshot()
	logs := s.logs.snapshot()

	seen := map[string]struct{}{}
	for i := range spans {
		seen[spans[i].Service] = struct{}{}
	}
	for i := range metrics {
		seen[metrics[i].Service] = struct{}{}
	}
	for i := range logs {
		seen[logs[i].Service] = struct{}{}
	}
	services := make([]string, 0, len(seen))
	for k := range seen {
		services = append(services, k)
	}
	sortStrings(services)

	rs, rm, rl := s.spans.received(), s.metrics.received(), s.logs.received()
	return Stats{
		SpansReceived: rs, MetricsReceived: rm, LogsReceived: rl,
		SpansHeld: len(spans), MetricsHeld: len(metrics), LogsHeld: len(logs),
		Services: services,
	}
}

// SpanQuery filters spans. Zero values mean "no filter".
type SpanQuery struct {
	Service string
	Name    string
	TraceID string
	Status  string
	Limit   int
}

// QuerySpans returns matching spans, newest first.
func (s *Store) QuerySpans(q SpanQuery) []model.Span {
	return filter(s.spans.snapshot(), q.Limit, func(v model.Span) bool {
		return matches(v.Service, q.Service) &&
			contains(v.Name, q.Name) &&
			matches(v.TraceID, q.TraceID) &&
			matches(v.StatusCode, q.Status)
	})
}

// MetricQuery filters metric streams.
type MetricQuery struct {
	Service string
	Name    string
	Type    string
	Limit   int
}

// QueryMetrics returns matching metric streams, newest first.
func (s *Store) QueryMetrics(q MetricQuery) []model.Metric {
	return filter(s.metrics.snapshot(), q.Limit, func(v model.Metric) bool {
		return matches(v.Service, q.Service) &&
			contains(v.Name, q.Name) &&
			matches(v.Type, q.Type)
	})
}

// LogQuery filters log records. MinSeverity uses OTLP severity numbers
// (1 TRACE, 5 DEBUG, 9 INFO, 13 WARN, 17 ERROR, 21 FATAL).
type LogQuery struct {
	Service     string
	Contains    string
	TraceID     string
	MinSeverity int32
	Limit       int
}

// QueryLogs returns matching log records, newest first.
func (s *Store) QueryLogs(q LogQuery) []model.Log {
	return filter(s.logs.snapshot(), q.Limit, func(v model.Log) bool {
		return matches(v.Service, q.Service) &&
			contains(v.Body, q.Contains) &&
			matches(v.TraceID, q.TraceID) &&
			v.SeverityNo >= q.MinSeverity
	})
}

// Trace returns every span held for one trace ID, oldest first.
func (s *Store) Trace(traceID string) []model.Span {
	all := s.spans.snapshot()
	out := make([]model.Span, 0, 16)
	for i := range all {
		if strings.EqualFold(all[i].TraceID, traceID) {
			out = append(out, all[i])
		}
	}
	return out
}

// Reset drops everything currently held. Counters keep running.
func (s *Store) Reset() {
	s.spans.reset()
	s.metrics.reset()
	s.logs.reset()
}

// filter walks newest-first and keeps up to limit matches.
func filter[T any](all []T, limit int, keep func(T) bool) []T {
	if limit <= 0 {
		limit = 100
	}
	out := make([]T, 0, limit)
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		if keep(all[i]) {
			out = append(out, all[i])
		}
	}
	return out
}

func matches(have, want string) bool {
	return want == "" || strings.EqualFold(have, want)
}

func contains(have, want string) bool {
	return want == "" || strings.Contains(strings.ToLower(have), strings.ToLower(want))
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ring is a fixed-capacity FIFO buffer. Once full, each new record
// overwrites the oldest one.
type ring[T any] struct {
	mu    sync.RWMutex
	buf   []T
	next  int
	full  bool
	total uint64
}

func newRing[T any](capacity int) *ring[T] {
	if capacity <= 0 {
		capacity = 1000
	}
	return &ring[T]{buf: make([]T, capacity)}
}

func (r *ring[T]) add(items []T) {
	if len(items) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total += uint64(len(items))
	// Only the last cap(buf) items can survive, so skip the rest.
	if len(items) > len(r.buf) {
		items = items[len(items)-len(r.buf):]
	}
	for i := range items {
		r.buf[r.next] = items[i]
		r.next++
		if r.next == len(r.buf) {
			r.next = 0
			r.full = true
		}
	}
}

// snapshot copies the held records, oldest first.
func (r *ring[T]) snapshot() []T {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.full {
		out := make([]T, r.next)
		copy(out, r.buf[:r.next])
		return out
	}
	out := make([]T, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	out = append(out, r.buf[:r.next]...)
	return out
}

func (r *ring[T]) received() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.total
}

func (r *ring[T]) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = make([]T, len(r.buf))
	r.next = 0
	r.full = false
}
