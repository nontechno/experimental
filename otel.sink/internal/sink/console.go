package sink

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nontechno/experimental/otel.sink/internal/config"
	"github.com/nontechno/experimental/otel.sink/internal/model"
)

// Console prints records to a writer. The mutex keeps records from
// interleaving when several exporters push at once.
type Console struct {
	mu        sync.Mutex
	w         io.Writer
	verbosity string
}

// NewConsole returns a Console at the given verbosity (see config.Verbosity*).
func NewConsole(w io.Writer, verbosity string) *Console {
	return &Console{w: w, verbosity: verbosity}
}

// Name implements Sink.
func (c *Console) Name() string { return "console" }

// Close implements Sink. The writer is owned by the caller.
func (c *Console) Close() error { return nil }

// Spans implements Sink.
func (c *Console) Spans(spans []model.Span) {
	if c.verbosity == config.VerbosityNone || len(spans) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.verbosity == config.VerbosityBasic {
		fmt.Fprintf(c.w, "%s  traces   %d span(s) from %s\n",
			stamp(), len(spans), servicesOf(len(spans), func(i int) string { return spans[i].Service }))
		return
	}
	for _, s := range spans {
		fmt.Fprintf(c.w, "%s  span     %-28s %-9s %8.2fms  %s trace=%s span=%s%s\n",
			stamp(), truncate(s.Name, 28), s.Service, s.DurationMS, statusMark(s.StatusCode),
			s.TraceID, s.SpanID, parentSuffix(s.ParentSpanID))
		if c.verbosity == config.VerbosityDetailed {
			c.writeAttrs("resource", s.Resource)
			c.writeAttrs("attributes", s.Attributes)
			for _, e := range s.Events {
				fmt.Fprintf(c.w, "             event  %s @ %s\n", e.Name, e.Time.UTC().Format(time.RFC3339Nano))
				c.writeAttrs("event.attributes", e.Attributes)
			}
			if s.StatusMessage != "" {
				fmt.Fprintf(c.w, "             status %s\n", s.StatusMessage)
			}
		}
	}
}

// Metrics implements Sink.
func (c *Console) Metrics(metrics []model.Metric) {
	if c.verbosity == config.VerbosityNone || len(metrics) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.verbosity == config.VerbosityBasic {
		points := 0
		for _, m := range metrics {
			points += len(m.DataPoints)
		}
		fmt.Fprintf(c.w, "%s  metrics  %d stream(s), %d point(s) from %s\n",
			stamp(), len(metrics), points,
			servicesOf(len(metrics), func(i int) string { return metrics[i].Service }))
		return
	}
	for _, m := range metrics {
		unit := m.Unit
		if unit != "" {
			unit = " " + unit
		}
		fmt.Fprintf(c.w, "%s  metric   %-28s %-9s %-11s %d point(s)%s\n",
			stamp(), truncate(m.Name, 28), m.Service, strings.ToLower(m.Type), len(m.DataPoints), unit)
		for _, p := range m.DataPoints {
			fmt.Fprintf(c.w, "             %s\n", formatPoint(p))
			if c.verbosity == config.VerbosityDetailed {
				c.writeAttrs("point.attributes", p.Attributes)
			}
		}
		if c.verbosity == config.VerbosityDetailed {
			c.writeAttrs("resource", m.Resource)
		}
	}
}

// Logs implements Sink.
func (c *Console) Logs(logs []model.Log) {
	if c.verbosity == config.VerbosityNone || len(logs) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.verbosity == config.VerbosityBasic {
		fmt.Fprintf(c.w, "%s  logs     %d record(s) from %s\n",
			stamp(), len(logs), servicesOf(len(logs), func(i int) string { return logs[i].Service }))
		return
	}
	for _, l := range logs {
		trace := ""
		if l.TraceID != "" {
			trace = " trace=" + l.TraceID
		}
		fmt.Fprintf(c.w, "%s  log      %-9s %-9s %s%s\n",
			stamp(), strings.ToUpper(l.Severity), l.Service, singleLine(l.Body), trace)
		if c.verbosity == config.VerbosityDetailed {
			c.writeAttrs("attributes", l.Attributes)
			c.writeAttrs("resource", l.Resource)
		}
	}
}

func (c *Console) writeAttrs(label string, attrs map[string]any) {
	if len(attrs) == 0 {
		return
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+scalar(attrs[k]))
	}
	fmt.Fprintf(c.w, "             %-16s %s\n", label, strings.Join(parts, " "))
}

func formatPoint(p model.DataPoint) string {
	var b strings.Builder
	switch {
	case len(p.Buckets) > 0 || len(p.Quantiles) > 0 || p.Count > 0:
		fmt.Fprintf(&b, "count=%d sum=%g", p.Count, p.Sum)
		if p.Count > 0 {
			fmt.Fprintf(&b, " avg=%g", float64(p.Sum)/float64(p.Count))
		}
		if p.Min != 0 || p.Max != 0 {
			fmt.Fprintf(&b, " min=%g max=%g", p.Min, p.Max)
		}
	default:
		fmt.Fprintf(&b, "value=%g", p.Value)
	}
	if len(p.Attributes) > 0 {
		b.WriteString(" {" + attrLine(p.Attributes) + "}")
	}
	if !p.Time.IsZero() {
		b.WriteString(" @ " + p.Time.UTC().Format(time.RFC3339))
	}
	return b.String()
}

func attrLine(attrs map[string]any) string {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+scalar(attrs[k]))
	}
	return strings.Join(parts, ", ")
}

func scalar(v any) string {
	switch t := v.(type) {
	case string:
		if strings.ContainsAny(t, " \t\"") {
			return fmt.Sprintf("%q", t)
		}
		return t
	case nil:
		return "null"
	case map[string]any, []any:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	default:
		return fmt.Sprint(t)
	}
}

func statusMark(code string) string {
	switch code {
	case "Error", "STATUS_CODE_ERROR":
		return "ERROR"
	case "Ok", "STATUS_CODE_OK":
		return "OK   "
	default:
		return "unset"
	}
}

func parentSuffix(parent string) string {
	if parent == "" {
		return " (root)"
	}
	return " parent=" + parent
}

// servicesOf lists up to three distinct service names in a batch.
func servicesOf(n int, at func(int) string) string {
	seen := make(map[string]struct{}, 4)
	names := make([]string, 0, 4)
	for i := 0; i < n; i++ {
		s := at(i)
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		names = append(names, s)
		if len(names) == 3 {
			break
		}
	}
	if len(seen) < n && len(names) == 3 {
		return strings.Join(names, ", ") + ", ..."
	}
	return strings.Join(names, ", ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "\u2026"
}

func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	return truncate(s, 120)
}

func stamp() string {
	return time.Now().UTC().Format("15:04:05.000")
}
