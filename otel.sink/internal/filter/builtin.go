package filter

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"

	"github.com/nontechno/experimental/otel.sink/internal/model"
)

func init() {
	Register("redact", newRedact)
	Register("min_duration", newMinDuration)
	Register("min_severity", newMinSeverity)
	Register("drop_names", newDropNames)
	Register("services", newServices)
	Register("sample", newSample)
	RegisterFuncs("errors_only", Funcs{
		Span: func(s model.Span) (model.Span, bool) {
			return s, strings.EqualFold(s.StatusCode, "Error")
		},
		Log: func(l model.Log) (model.Log, bool) {
			return l, l.SeverityNo >= severityError
		},
		// Metric is nil: metrics pass through untouched.
	})
}

const severityError = 17

// redact replaces the values of named attribute keys. Useful when a service
// puts credentials or full SQL in span attributes and you want the traces
// without them.
//
//	options: keys=db.statement,http.url  [value=[REDACTED]]
func newRedact(options map[string]string) (Filter, error) {
	keys := splitList(options["keys"])
	if len(keys) == 0 {
		return nil, fmt.Errorf("needs keys=attr1,attr2")
	}
	value := options["value"]
	if value == "" {
		value = "[REDACTED]"
	}
	set := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		set[k] = struct{}{}
	}
	scrub := func(attrs map[string]any) {
		for k := range attrs {
			if _, hit := set[k]; hit {
				attrs[k] = value
			}
		}
	}
	return Funcs{
		FilterName: "redact",
		Span: func(s model.Span) (model.Span, bool) {
			scrub(s.Attributes)
			scrub(s.Resource)
			for i := range s.Events {
				scrub(s.Events[i].Attributes)
			}
			return s, true
		},
		Metric: func(m model.Metric) (model.Metric, bool) {
			scrub(m.Resource)
			for i := range m.DataPoints {
				scrub(m.DataPoints[i].Attributes)
			}
			return m, true
		},
		Log: func(l model.Log) (model.Log, bool) {
			scrub(l.Attributes)
			scrub(l.Resource)
			return l, true
		},
	}, nil
}

// min_duration drops spans faster than a threshold, which is the quickest
// way to cut health-check noise out of a busy trace stream.
//
//	options: ms=50
func newMinDuration(options map[string]string) (Filter, error) {
	ms, err := strconv.ParseFloat(orDefault(options["ms"], "0"), 64)
	if err != nil {
		return nil, fmt.Errorf("ms: %w", err)
	}
	return Funcs{
		FilterName: "min_duration",
		Span: func(s model.Span) (model.Span, bool) {
			return s, s.DurationMS >= ms
		},
	}, nil
}

// min_severity drops log records below a level, given as an OTLP severity
// number or a name (trace, debug, info, warn, error, fatal).
//
//	options: severity=warn
func newMinSeverity(options map[string]string) (Filter, error) {
	raw := orDefault(options["severity"], "info")
	level, err := parseSeverity(raw)
	if err != nil {
		return nil, err
	}
	return Funcs{
		FilterName: "min_severity",
		Log: func(l model.Log) (model.Log, bool) {
			return l, l.SeverityNo >= level
		},
	}, nil
}

// drop_names drops spans and metrics whose name contains any of the given
// substrings. Matching is case-insensitive.
//
//	options: contains=/health,/metrics,runtime.jvm
func newDropNames(options map[string]string) (Filter, error) {
	needles := splitList(strings.ToLower(options["contains"]))
	if len(needles) == 0 {
		return nil, fmt.Errorf("needs contains=substr1,substr2")
	}
	matches := func(name string) bool {
		lower := strings.ToLower(name)
		for _, n := range needles {
			if strings.Contains(lower, n) {
				return true
			}
		}
		return false
	}
	return Funcs{
		FilterName: "drop_names",
		Span:       func(s model.Span) (model.Span, bool) { return s, !matches(s.Name) },
		Metric:     func(m model.Metric) (model.Metric, bool) { return m, !matches(m.Name) },
	}, nil
}

// services keeps or drops whole services across all three signals.
//
//	options: allow=checkout,payments   or   deny=noisy-batch-job
func newServices(options map[string]string) (Filter, error) {
	allow := lowerSet(splitList(options["allow"]))
	deny := lowerSet(splitList(options["deny"]))
	if len(allow) == 0 && len(deny) == 0 {
		return nil, fmt.Errorf("needs allow=... or deny=...")
	}
	if len(allow) > 0 && len(deny) > 0 {
		return nil, fmt.Errorf("set allow or deny, not both")
	}
	keep := func(service string) bool {
		s := strings.ToLower(service)
		if len(allow) > 0 {
			_, ok := allow[s]
			return ok
		}
		_, blocked := deny[s]
		return !blocked
	}
	return Funcs{
		FilterName: "services",
		Span:       func(s model.Span) (model.Span, bool) { return s, keep(s.Service) },
		Metric:     func(m model.Metric) (model.Metric, bool) { return m, keep(m.Service) },
		Log:        func(l model.Log) (model.Log, bool) { return l, keep(l.Service) },
	}, nil
}

// sample keeps a fraction of traces. The decision hashes the trace ID rather
// than sampling per span, so a kept trace keeps all of its spans and the
// waterfall in the dashboard stays intact.
//
//	options: ratio=0.1
func newSample(options map[string]string) (Filter, error) {
	ratio, err := strconv.ParseFloat(orDefault(options["ratio"], "1"), 64)
	if err != nil {
		return nil, fmt.Errorf("ratio: %w", err)
	}
	if ratio < 0 || ratio > 1 {
		return nil, fmt.Errorf("ratio %v: want 0..1", ratio)
	}
	threshold := uint64(ratio * float64(uint64(1)<<53))
	return Funcs{
		FilterName: "sample",
		Span: func(s model.Span) (model.Span, bool) {
			if ratio >= 1 {
				return s, true
			}
			if ratio <= 0 {
				return s, false
			}
			return s, mix64(hashString(s.TraceID))>>11 < threshold
		},
	}, nil
}

func hashString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// mix64 is the splitmix64 finalizer. FNV-1a alone leaves its high bits
// poorly distributed for short, similar inputs — and trace IDs from a single
// service often share structure — so sampling on the raw high bits skews
// badly. This avalanches the whole word first.
func mix64(x uint64) uint64 {
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 33
	return x
}

func parseSeverity(raw string) (int32, error) {
	named := map[string]int32{
		"trace": 1, "debug": 5, "info": 9, "warn": 13,
		"warning": 13, "error": severityError, "fatal": 21,
	}
	if level, ok := named[strings.ToLower(strings.TrimSpace(raw))]; ok {
		return level, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("severity %q: want a number or trace/debug/info/warn/error/fatal", raw)
	}
	return int32(n), nil
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func lowerSet(items []string) map[string]struct{} {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(items))
	for _, item := range items {
		out[strings.ToLower(item)] = struct{}{}
	}
	return out
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
