package sink

import (
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nontechno/experimental/otel.sink/internal/model"
)

// Forwarding in filtered mode has to reconcile two representations of the
// same batch: the pdata that will actually be sent, and the flattened
// records the filter chain operated on.
//
// The link is model.Record.Index, the ordinal assigned during flattening.
// Flattening and the pruning walks below traverse the batch in exactly the
// same order, so an index identifies the same record in both. Filters may
// drop records but never reorder or renumber them.
//
// Two things are reconciled:
//
//   - dropped records are removed from the pdata, along with any scope or
//     resource left empty;
//   - attribute edits are copied back, so a redact filter actually redacts
//     what leaves the process. Silently forwarding the unredacted original
//     would turn a filter into a data leak.

// pruneTraces removes dropped spans and copies attribute edits back.
func pruneTraces(td ptrace.Traces, kept []model.Span) {
	index := make(map[int]*model.Span, len(kept))
	for i := range kept {
		index[kept[i].Index] = &kept[i]
	}

	pos := 0
	td.ResourceSpans().RemoveIf(func(rs ptrace.ResourceSpans) bool {
		rs.ScopeSpans().RemoveIf(func(ss ptrace.ScopeSpans) bool {
			ss.Spans().RemoveIf(func(span ptrace.Span) bool {
				rec, ok := index[pos]
				pos++
				if !ok {
					return true // filtered out
				}
				setAttrs(span.Attributes(), rec.Attributes)
				events := span.Events()
				for i := 0; i < events.Len() && i < len(rec.Events); i++ {
					setAttrs(events.At(i).Attributes(), rec.Events[i].Attributes)
				}
				return false
			})
			return ss.Spans().Len() == 0
		})
		return rs.ScopeSpans().Len() == 0
	})
}

// pruneMetrics removes dropped metric streams and copies attribute edits
// back onto their data points.
func pruneMetrics(md pmetric.Metrics, kept []model.Metric) {
	index := make(map[int]*model.Metric, len(kept))
	for i := range kept {
		index[kept[i].Index] = &kept[i]
	}

	pos := 0
	md.ResourceMetrics().RemoveIf(func(rm pmetric.ResourceMetrics) bool {
		rm.ScopeMetrics().RemoveIf(func(sm pmetric.ScopeMetrics) bool {
			sm.Metrics().RemoveIf(func(m pmetric.Metric) bool {
				rec, ok := index[pos]
				pos++
				if !ok {
					return true
				}
				syncDataPointAttrs(m, rec)
				return false
			})
			return sm.Metrics().Len() == 0
		})
		return rm.ScopeMetrics().Len() == 0
	})
}

// pruneLogs removes dropped log records and copies attribute edits back.
func pruneLogs(ld plog.Logs, kept []model.Log) {
	index := make(map[int]*model.Log, len(kept))
	for i := range kept {
		index[kept[i].Index] = &kept[i]
	}

	pos := 0
	ld.ResourceLogs().RemoveIf(func(rl plog.ResourceLogs) bool {
		rl.ScopeLogs().RemoveIf(func(sl plog.ScopeLogs) bool {
			sl.LogRecords().RemoveIf(func(lr plog.LogRecord) bool {
				rec, ok := index[pos]
				pos++
				if !ok {
					return true
				}
				setAttrs(lr.Attributes(), rec.Attributes)
				return false
			})
			return sl.LogRecords().Len() == 0
		})
		return rl.ScopeLogs().Len() == 0
	})
}

// syncDataPointAttrs copies edited attributes onto whichever data point
// slice this metric type carries. Data points keep their order through
// flattening, so index i matches.
func syncDataPointAttrs(m pmetric.Metric, rec *model.Metric) {
	if len(rec.DataPoints) == 0 {
		return
	}
	at := func(i int) map[string]any {
		if i < len(rec.DataPoints) {
			return rec.DataPoints[i].Attributes
		}
		return nil
	}
	switch m.Type() {
	case pmetric.MetricTypeGauge:
		dps := m.Gauge().DataPoints()
		for i := 0; i < dps.Len(); i++ {
			setAttrs(dps.At(i).Attributes(), at(i))
		}
	case pmetric.MetricTypeSum:
		dps := m.Sum().DataPoints()
		for i := 0; i < dps.Len(); i++ {
			setAttrs(dps.At(i).Attributes(), at(i))
		}
	case pmetric.MetricTypeHistogram:
		dps := m.Histogram().DataPoints()
		for i := 0; i < dps.Len(); i++ {
			setAttrs(dps.At(i).Attributes(), at(i))
		}
	case pmetric.MetricTypeExponentialHistogram:
		dps := m.ExponentialHistogram().DataPoints()
		for i := 0; i < dps.Len(); i++ {
			setAttrs(dps.At(i).Attributes(), at(i))
		}
	case pmetric.MetricTypeSummary:
		dps := m.Summary().DataPoints()
		for i := 0; i < dps.Len(); i++ {
			setAttrs(dps.At(i).Attributes(), at(i))
		}
	}
}

// setAttrs overwrites a pdata attribute map from the (possibly edited)
// flattened copy. A nil map means the record carried no attributes, which
// is different from a filter having cleared them, so it is left alone.
func setAttrs(dst pcommon.Map, src map[string]any) {
	if src == nil {
		return
	}
	_ = dst.FromRaw(src)
}
