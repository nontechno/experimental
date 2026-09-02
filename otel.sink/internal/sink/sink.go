// Package sink defines the single interface every output implements, and
// the Fanout that feeds them.
//
// The flow for each received export is:
//
//	OTLP batch -> model.Flatten* -> filter.Chain -> every Sink
//
// Flattening happens once per batch, filters run once per batch ahead of all
// outputs, and each sink then sees the same records. Adding an output means
// implementing Sink; changing what the outputs see means adding a filter.
package sink

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nontechno/experimental/otel.sink/internal/filter"
	"github.com/nontechno/experimental/otel.sink/internal/model"
)

// Sink is one output: the console, a file, a socket, the in-memory store.
//
// Implementations must be safe for concurrent use, since gRPC and HTTP
// exports are served in parallel. They receive records after filtering and
// must not retain the slice they are handed — copy any record kept beyond
// the call, as the Fanout reuses batch slices.
type Sink interface {
	// Name identifies the sink in logs and errors.
	Name() string
	Spans([]model.Span)
	Metrics([]model.Metric)
	Logs([]model.Log)
	// Close releases resources. It must be safe to call more than once.
	Close() error
}

// Exporter forwards a batch to another OTLP endpoint. Unlike a Sink it
// receives pdata rather than flattened records, so nothing is lost in
// translation on the way through.
type Exporter interface {
	Name() string
	ExportTraces(context.Context, ptrace.Traces) error
	ExportMetrics(context.Context, pmetric.Metrics) error
	ExportLogs(context.Context, plog.Logs) error
	Close() error
}

// Forward modes.
const (
	// ForwardRaw sends the batch exactly as received. Filters do not apply:
	// a filter that drops or redacts affects local outputs only.
	ForwardRaw = "raw"
	// ForwardFiltered prunes the batch to what the filter chain kept and
	// copies attribute edits back, so upstream sees what the local outputs
	// see. Costs one extra pass over the batch.
	ForwardFiltered = "filtered"
)

// Consumer is what the OTLP receivers call. Fanout is the implementation;
// the interface exists so receivers do not import sinks or filters.
type Consumer interface {
	ConsumeTraces(context.Context, ptrace.Traces) error
	ConsumeMetrics(context.Context, pmetric.Metrics) error
	ConsumeLogs(context.Context, plog.Logs) error
}

// Fanout flattens each batch, runs it through the filter chain, and hands
// the survivors to every sink in order.
//
// Sinks run inline, so a slow sink back-pressures the sender rather than
// dropping data silently. A sink that must not block the export path (the
// UDS stream, for instance) is responsible for buffering internally.
type Fanout struct {
	filters     filter.Chain
	sinks       []Sink
	exporters   []Exporter
	forwardMode string
	now         func() time.Time

	filteredSpans   atomic.Uint64
	filteredMetrics atomic.Uint64
	filteredLogs    atomic.Uint64
}

// FanoutConfig wires the pipeline together.
type FanoutConfig struct {
	Filters filter.Chain
	Sinks   []Sink
	// Exporters forward the batch onward, making otel.sink a proxy.
	Exporters []Exporter
	// ForwardMode is ForwardRaw or ForwardFiltered.
	ForwardMode string
}

// NewFanout returns a Consumer that filters, writes to all sinks, and
// forwards to any exporters.
func NewFanout(cfg FanoutConfig) *Fanout {
	mode := cfg.ForwardMode
	if mode == "" {
		mode = ForwardRaw
	}
	return &Fanout{
		filters:     cfg.Filters,
		sinks:       cfg.Sinks,
		exporters:   cfg.Exporters,
		forwardMode: mode,
		now:         time.Now,
	}
}

// ConsumeTraces implements Consumer.
func (f *Fanout) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
	if td.SpanCount() == 0 {
		return nil
	}
	records := model.FlattenTraces(td, f.now().UTC())
	before := len(records)
	records = f.filters.Spans(records)
	f.filteredSpans.Add(uint64(before - len(records)))

	for _, s := range f.sinks {
		if len(records) > 0 {
			s.Spans(records)
		}
	}
	if len(f.exporters) == 0 {
		return nil
	}
	if f.forwardMode == ForwardFiltered {
		if len(records) == 0 {
			return nil // everything was filtered out; nothing to forward
		}
		// Always prune, even when nothing was dropped: filters may have
		// edited attributes, and those edits must reach upstream too.
		pruneTraces(td, records)
		if td.SpanCount() == 0 {
			return nil
		}
	}
	var errs []error
	for _, e := range f.exporters {
		if err := e.ExportTraces(ctx, td); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ConsumeMetrics implements Consumer.
func (f *Fanout) ConsumeMetrics(ctx context.Context, md pmetric.Metrics) error {
	if md.DataPointCount() == 0 {
		return nil
	}
	records := model.FlattenMetrics(md, f.now().UTC())
	before := len(records)
	records = f.filters.Metrics(records)
	f.filteredMetrics.Add(uint64(before - len(records)))

	for _, s := range f.sinks {
		if len(records) > 0 {
			s.Metrics(records)
		}
	}
	if len(f.exporters) == 0 {
		return nil
	}
	if f.forwardMode == ForwardFiltered {
		if len(records) == 0 {
			return nil
		}
		pruneMetrics(md, records)
		if md.DataPointCount() == 0 {
			return nil
		}
	}
	var errs []error
	for _, e := range f.exporters {
		if err := e.ExportMetrics(ctx, md); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ConsumeLogs implements Consumer.
func (f *Fanout) ConsumeLogs(ctx context.Context, ld plog.Logs) error {
	if ld.LogRecordCount() == 0 {
		return nil
	}
	records := model.FlattenLogs(ld, f.now().UTC())
	before := len(records)
	records = f.filters.Logs(records)
	f.filteredLogs.Add(uint64(before - len(records)))

	for _, s := range f.sinks {
		if len(records) > 0 {
			s.Logs(records)
		}
	}
	if len(f.exporters) == 0 {
		return nil
	}
	if f.forwardMode == ForwardFiltered {
		if len(records) == 0 {
			return nil
		}
		pruneLogs(ld, records)
		if ld.LogRecordCount() == 0 {
			return nil
		}
	}
	var errs []error
	for _, e := range f.exporters {
		if err := e.ExportLogs(ctx, ld); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Filtered reports how many records the chain dropped, per signal.
func (f *Fanout) Filtered() (spans, metrics, logs uint64) {
	return f.filteredSpans.Load(), f.filteredMetrics.Load(), f.filteredLogs.Load()
}

// Names lists the sinks and exporters, for logging.
func (f *Fanout) Names() []string {
	out := make([]string, 0, len(f.sinks)+len(f.exporters))
	for _, s := range f.sinks {
		out = append(out, s.Name())
	}
	for _, e := range f.exporters {
		out = append(out, e.Name())
	}
	return out
}

// Close closes every sink, collecting errors rather than stopping at the
// first: a failure to flush one output should not leak another's socket.
func (f *Fanout) Close() error {
	var errs []error
	for _, s := range f.sinks {
		if err := s.Close(); err != nil {
			errs = append(errs, errors.New(s.Name()+": "+err.Error()))
		}
	}
	for _, e := range f.exporters {
		if err := e.Close(); err != nil {
			errs = append(errs, errors.New(e.Name()+": "+err.Error()))
		}
	}
	return errors.Join(errs...)
}
