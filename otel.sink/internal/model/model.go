// Package model converts OTLP payloads (pdata) into flat, JSON-friendly
// records. Everything downstream — the console printer, the file writer, the
// in-memory store and the query API — works with these types rather than with
// pdata, so the pdata traversal lives in exactly one place.
package model

import (
	"encoding/json"
	"math"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// UnknownService is used when a resource carries no service.name attribute.
const UnknownService = "unknown_service"

// Float is a float64 that survives JSON encoding. JSON has no
// representation for NaN or +/-Inf, and encoding/json fails the whole
// document rather than skipping the value, so those encode as null. This
// matters in practice: the overflow bucket of every explicit histogram has
// an upper bound of +Inf.
type Float float64

// MarshalJSON implements json.Marshaler.
func (f Float) MarshalJSON() ([]byte, error) {
	v := float64(f)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return []byte("null"), nil
	}
	return json.Marshal(v)
}

// UnmarshalJSON implements json.Unmarshaler, reading null back as +Inf so
// that a histogram round-trips through the JSONL files.
func (f *Float) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*f = Float(math.Inf(1))
		return nil
	}
	var v float64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = Float(v)
	return nil
}

// Span is one finished span.
type Span struct {
	// Index is the span's ordinal within the batch it arrived in. It links
	// a flattened record back to its pdata original when forwarding, and is
	// not part of the wire format.
	Index         int            `json:"-"`
	Received      time.Time      `json:"received"`
	TraceID       string         `json:"trace_id"`
	SpanID        string         `json:"span_id"`
	ParentSpanID  string         `json:"parent_span_id,omitempty"`
	Name          string         `json:"name"`
	Kind          string         `json:"kind"`
	Service       string         `json:"service"`
	Scope         string         `json:"scope,omitempty"`
	Start         time.Time      `json:"start"`
	End           time.Time      `json:"end"`
	DurationMS    float64        `json:"duration_ms"`
	StatusCode    string         `json:"status_code"`
	StatusMessage string         `json:"status_message,omitempty"`
	Attributes    map[string]any `json:"attributes,omitempty"`
	Resource      map[string]any `json:"resource,omitempty"`
	Events        []SpanEvent    `json:"events,omitempty"`
}

// SpanEvent is a timestamped event recorded on a span.
type SpanEvent struct {
	Time       time.Time      `json:"time"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// Metric is one metric stream from one resource and instrumentation scope,
// together with the data points carried in the batch that produced it.
type Metric struct {
	// Index is the stream's ordinal within its batch. See Span.Index.
	Index       int            `json:"-"`
	Received    time.Time      `json:"received"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Unit        string         `json:"unit,omitempty"`
	Type        string         `json:"type"`
	Temporality string         `json:"temporality,omitempty"`
	Monotonic   bool           `json:"monotonic,omitempty"`
	Service     string         `json:"service"`
	Scope       string         `json:"scope,omitempty"`
	Resource    map[string]any `json:"resource,omitempty"`
	DataPoints  []DataPoint    `json:"data_points"`
}

// DataPoint carries whichever fields the metric type populates. Gauges and
// sums use Value; histograms and summaries use Count, Sum, Min, Max and
// Buckets or Quantiles.
type DataPoint struct {
	Time       time.Time      `json:"time"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Value      Float          `json:"value,omitempty"`
	Count      uint64         `json:"count,omitempty"`
	Sum        Float          `json:"sum,omitempty"`
	Min        Float          `json:"min,omitempty"`
	Max        Float          `json:"max,omitempty"`
	Buckets    []Bucket       `json:"buckets,omitempty"`
	Quantiles  []Quantile     `json:"quantiles,omitempty"`
}

// Bucket is one explicit histogram bucket. UpperBound is +Inf for the
// overflow bucket.
type Bucket struct {
	UpperBound Float  `json:"upper_bound"`
	Count      uint64 `json:"count"`
}

// Quantile is one summary quantile.
type Quantile struct {
	Quantile Float `json:"quantile"`
	Value    Float `json:"value"`
}

// Log is one log record.
type Log struct {
	// Index is the record's ordinal within its batch. See Span.Index.
	Index      int            `json:"-"`
	Received   time.Time      `json:"received"`
	Time       time.Time      `json:"time"`
	Severity   string         `json:"severity"`
	SeverityNo int32          `json:"severity_number"`
	Body       string         `json:"body"`
	Service    string         `json:"service"`
	Scope      string         `json:"scope,omitempty"`
	TraceID    string         `json:"trace_id,omitempty"`
	SpanID     string         `json:"span_id,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Resource   map[string]any `json:"resource,omitempty"`
}

// FlattenTraces converts an OTLP trace batch into Span records.
func FlattenTraces(td ptrace.Traces, received time.Time) []Span {
	out := make([]Span, 0, td.SpanCount())
	rss := td.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		rs := rss.At(i)
		resource := rs.Resource().Attributes().AsRaw()
		service := serviceName(rs.Resource())
		sss := rs.ScopeSpans()
		for j := 0; j < sss.Len(); j++ {
			ss := sss.At(j)
			scope := ss.Scope().Name()
			spans := ss.Spans()
			for k := 0; k < spans.Len(); k++ {
				s := spans.At(k)
				start := s.StartTimestamp().AsTime()
				end := s.EndTimestamp().AsTime()
				rec := Span{
					Index:         len(out),
					Received:      received,
					TraceID:       s.TraceID().String(),
					SpanID:        s.SpanID().String(),
					Name:          s.Name(),
					Kind:          s.Kind().String(),
					Service:       service,
					Scope:         scope,
					Start:         start,
					End:           end,
					DurationMS:    float64(end.Sub(start).Nanoseconds()) / 1e6,
					StatusCode:    s.Status().Code().String(),
					StatusMessage: s.Status().Message(),
					Attributes:    s.Attributes().AsRaw(),
					Resource:      resource,
				}
				if parent := s.ParentSpanID(); !parent.IsEmpty() {
					rec.ParentSpanID = parent.String()
				}
				events := s.Events()
				for e := 0; e < events.Len(); e++ {
					ev := events.At(e)
					rec.Events = append(rec.Events, SpanEvent{
						Time:       ev.Timestamp().AsTime(),
						Name:       ev.Name(),
						Attributes: ev.Attributes().AsRaw(),
					})
				}
				out = append(out, rec)
			}
		}
	}
	return out
}

// FlattenMetrics converts an OTLP metric batch into Metric records.
func FlattenMetrics(md pmetric.Metrics, received time.Time) []Metric {
	out := make([]Metric, 0, md.MetricCount())
	rms := md.ResourceMetrics()
	for i := 0; i < rms.Len(); i++ {
		rm := rms.At(i)
		resource := rm.Resource().Attributes().AsRaw()
		service := serviceName(rm.Resource())
		sms := rm.ScopeMetrics()
		for j := 0; j < sms.Len(); j++ {
			sm := sms.At(j)
			scope := sm.Scope().Name()
			metrics := sm.Metrics()
			for k := 0; k < metrics.Len(); k++ {
				m := metrics.At(k)
				rec := Metric{
					Index:       len(out),
					Received:    received,
					Name:        m.Name(),
					Description: m.Description(),
					Unit:        m.Unit(),
					Type:        m.Type().String(),
					Service:     service,
					Scope:       scope,
					Resource:    resource,
				}
				switch m.Type() {
				case pmetric.MetricTypeGauge:
					rec.DataPoints = numberPoints(m.Gauge().DataPoints())
				case pmetric.MetricTypeSum:
					sum := m.Sum()
					rec.Monotonic = sum.IsMonotonic()
					rec.Temporality = sum.AggregationTemporality().String()
					rec.DataPoints = numberPoints(sum.DataPoints())
				case pmetric.MetricTypeHistogram:
					h := m.Histogram()
					rec.Temporality = h.AggregationTemporality().String()
					rec.DataPoints = histogramPoints(h.DataPoints())
				case pmetric.MetricTypeExponentialHistogram:
					eh := m.ExponentialHistogram()
					rec.Temporality = eh.AggregationTemporality().String()
					rec.DataPoints = expHistogramPoints(eh.DataPoints())
				case pmetric.MetricTypeSummary:
					rec.DataPoints = summaryPoints(m.Summary().DataPoints())
				default:
					rec.DataPoints = nil
				}
				out = append(out, rec)
			}
		}
	}
	return out
}

// FlattenLogs converts an OTLP log batch into Log records.
func FlattenLogs(ld plog.Logs, received time.Time) []Log {
	out := make([]Log, 0, ld.LogRecordCount())
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		rl := rls.At(i)
		resource := rl.Resource().Attributes().AsRaw()
		service := serviceName(rl.Resource())
		sls := rl.ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			sl := sls.At(j)
			scope := sl.Scope().Name()
			records := sl.LogRecords()
			for k := 0; k < records.Len(); k++ {
				lr := records.At(k)
				ts := lr.Timestamp().AsTime()
				if lr.Timestamp() == 0 {
					ts = lr.ObservedTimestamp().AsTime()
				}
				severity := lr.SeverityText()
				if severity == "" {
					severity = lr.SeverityNumber().String()
				}
				rec := Log{
					Index:      len(out),
					Received:   received,
					Time:       ts,
					Severity:   severity,
					SeverityNo: int32(lr.SeverityNumber()),
					Body:       lr.Body().AsString(),
					Service:    service,
					Scope:      scope,
					Attributes: lr.Attributes().AsRaw(),
					Resource:   resource,
				}
				if id := lr.TraceID(); !id.IsEmpty() {
					rec.TraceID = id.String()
				}
				if id := lr.SpanID(); !id.IsEmpty() {
					rec.SpanID = id.String()
				}
				out = append(out, rec)
			}
		}
	}
	return out
}

func serviceName(res pcommon.Resource) string {
	if v, ok := res.Attributes().Get("service.name"); ok {
		if s := v.AsString(); s != "" {
			return s
		}
	}
	return UnknownService
}

func numberPoints(dps pmetric.NumberDataPointSlice) []DataPoint {
	out := make([]DataPoint, 0, dps.Len())
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		p := DataPoint{Time: dp.Timestamp().AsTime(), Attributes: dp.Attributes().AsRaw()}
		switch dp.ValueType() {
		case pmetric.NumberDataPointValueTypeInt:
			p.Value = Float(dp.IntValue())
		case pmetric.NumberDataPointValueTypeDouble:
			p.Value = Float(dp.DoubleValue())
		}
		out = append(out, p)
	}
	return out
}

func histogramPoints(dps pmetric.HistogramDataPointSlice) []DataPoint {
	out := make([]DataPoint, 0, dps.Len())
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		p := DataPoint{
			Time:       dp.Timestamp().AsTime(),
			Attributes: dp.Attributes().AsRaw(),
			Count:      dp.Count(),
		}
		if dp.HasSum() {
			p.Sum = Float(dp.Sum())
		}
		if dp.HasMin() {
			p.Min = Float(dp.Min())
		}
		if dp.HasMax() {
			p.Max = Float(dp.Max())
		}
		bounds := dp.ExplicitBounds().AsRaw()
		counts := dp.BucketCounts().AsRaw()
		for b := 0; b < len(counts); b++ {
			upper := math.Inf(1)
			if b < len(bounds) {
				upper = bounds[b]
			}
			p.Buckets = append(p.Buckets, Bucket{UpperBound: Float(upper), Count: counts[b]})
		}
		out = append(out, p)
	}
	return out
}

// expHistogramPoints keeps the summary statistics of an exponential
// histogram. The scaled bucket layout is deliberately not expanded here.
func expHistogramPoints(dps pmetric.ExponentialHistogramDataPointSlice) []DataPoint {
	out := make([]DataPoint, 0, dps.Len())
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		p := DataPoint{
			Time:       dp.Timestamp().AsTime(),
			Attributes: dp.Attributes().AsRaw(),
			Count:      dp.Count(),
		}
		if dp.HasSum() {
			p.Sum = Float(dp.Sum())
		}
		if dp.HasMin() {
			p.Min = Float(dp.Min())
		}
		if dp.HasMax() {
			p.Max = Float(dp.Max())
		}
		out = append(out, p)
	}
	return out
}

func summaryPoints(dps pmetric.SummaryDataPointSlice) []DataPoint {
	out := make([]DataPoint, 0, dps.Len())
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		p := DataPoint{
			Time:       dp.Timestamp().AsTime(),
			Attributes: dp.Attributes().AsRaw(),
			Count:      dp.Count(),
			Sum:        Float(dp.Sum()),
		}
		qs := dp.QuantileValues()
		for q := 0; q < qs.Len(); q++ {
			p.Quantiles = append(p.Quantiles, Quantile{
				Quantile: Float(qs.At(q).Quantile()),
				Value:    Float(qs.At(q).Value()),
			})
		}
		out = append(out, p)
	}
	return out
}
