// Package filter lets you transform or drop telemetry before it reaches any
// sink. Filters run inside the Fanout, ahead of every output, so the console,
// the files, the UDS stream and the dashboard all see the same filtered data.
//
// There are two ways to add one:
//
//  1. Write a Funcs value and register it. See custom.go for a worked
//     example; the whole thing can be a dozen lines.
//  2. Implement Filter directly, when a filter needs to look at a whole
//     batch at once (deduplication, rate limiting, aggregation).
//
// Filters are compiled in, not loaded at runtime: register yours in an
// init() and select it by name in the config.
package filter

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/nontechno/experimental/otel.sink/internal/model"
)

// Filter transforms a batch of records. Returning a shorter slice drops
// records; mutating a record in place edits it.
//
// A filter owns the slice it is given and may modify it in place — the
// Fanout builds a fresh one per batch and hands it to the chain before any
// sink sees it. Implementations must be safe for concurrent use, since
// exports arrive in parallel.
type Filter interface {
	Name() string
	Spans([]model.Span) []model.Span
	Metrics([]model.Metric) []model.Metric
	Logs([]model.Log) []model.Log
}

// Funcs builds a Filter from per-record functions. A nil function passes
// that signal through untouched, so a spans-only filter sets only Span.
// Each function returns the (possibly edited) record and whether to keep it.
type Funcs struct {
	FilterName string
	Span       func(model.Span) (model.Span, bool)
	Metric     func(model.Metric) (model.Metric, bool)
	Log        func(model.Log) (model.Log, bool)
}

// Name implements Filter.
func (f Funcs) Name() string {
	if f.FilterName == "" {
		return "anonymous"
	}
	return f.FilterName
}

// Spans implements Filter.
func (f Funcs) Spans(in []model.Span) []model.Span { return apply(in, f.Span) }

// Metrics implements Filter.
func (f Funcs) Metrics(in []model.Metric) []model.Metric { return apply(in, f.Metric) }

// Logs implements Filter.
func (f Funcs) Logs(in []model.Log) []model.Log { return apply(in, f.Log) }

// apply compacts in place: the surviving records are moved to the front of
// the caller's slice, which avoids an allocation per batch per filter.
func apply[T any](in []T, fn func(T) (T, bool)) []T {
	if fn == nil || len(in) == 0 {
		return in
	}
	kept := in[:0]
	for i := range in {
		if rec, keep := fn(in[i]); keep {
			kept = append(kept, rec)
		}
	}
	return kept
}

// Chain runs filters in order and short-circuits once nothing is left.
type Chain []Filter

// Spans runs the chain over a span batch.
func (c Chain) Spans(in []model.Span) []model.Span {
	for _, f := range c {
		if len(in) == 0 {
			return in
		}
		in = f.Spans(in)
	}
	return in
}

// Metrics runs the chain over a metric batch.
func (c Chain) Metrics(in []model.Metric) []model.Metric {
	for _, f := range c {
		if len(in) == 0 {
			return in
		}
		in = f.Metrics(in)
	}
	return in
}

// Logs runs the chain over a log batch.
func (c Chain) Logs(in []model.Log) []model.Log {
	for _, f := range c {
		if len(in) == 0 {
			return in
		}
		in = f.Logs(in)
	}
	return in
}

// Names lists the filters in the chain, for logging.
func (c Chain) Names() []string {
	out := make([]string, 0, len(c))
	for _, f := range c {
		out = append(out, f.Name())
	}
	return out
}

// Factory builds a filter from its configured options.
type Factory func(options map[string]string) (Filter, error)

// Spec selects a filter by registered name and passes it options.
type Spec struct {
	Name    string
	Options map[string]string
}

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register makes a filter available to the config by name. Call it from an
// init(). Registering the same name twice panics, since that is a
// programming mistake rather than a runtime condition.
func Register(name string, factory Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[name]; exists {
		panic("filter: duplicate registration of " + name)
	}
	registry[name] = factory
}

// RegisterFuncs is the shorthand for a filter with no options.
func RegisterFuncs(name string, f Funcs) {
	if f.FilterName == "" {
		f.FilterName = name
	}
	Register(name, func(map[string]string) (Filter, error) { return f, nil })
}

// Registered lists every known filter name, sorted.
func Registered() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Build turns configured specs into a chain, in order.
func Build(specs []Spec) (Chain, error) {
	chain := make(Chain, 0, len(specs))
	for _, spec := range specs {
		mu.RLock()
		factory, ok := registry[spec.Name]
		mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("unknown filter %q: registered filters are %s",
				spec.Name, strings.Join(Registered(), ", "))
		}
		f, err := factory(spec.Options)
		if err != nil {
			return nil, fmt.Errorf("filter %q: %w", spec.Name, err)
		}
		chain = append(chain, f)
	}
	return chain, nil
}
