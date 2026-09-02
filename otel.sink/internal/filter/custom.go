package filter

import (
	"strings"

	"github.com/nontechno/experimental/otel.sink/internal/model"
)

// This file is the place to put your own processing. Everything here is
// ordinary Go: no plugin loading, no reflection. Add a function, register it
// in init(), name it in the config, rebuild.
//
//	filters:
//	  - name: tag_slow_spans
//
// Two shapes are available.
//
// Shape 1 — per record, no options. Return the record and whether to keep
// it. Set only the signals you care about; nil fields pass through.
func init() {
	RegisterFuncs("tag_slow_spans", Funcs{
		Span: func(s model.Span) (model.Span, bool) {
			if s.DurationMS > 500 {
				if s.Attributes == nil {
					s.Attributes = map[string]any{}
				}
				s.Attributes["slow"] = true
			}
			return s, true
		},
	})

	// Shape 2 — with options from the config, via a factory:
	//
	//	filters:
	//	  - name: only_paths
	//	    options:
	//	      prefix: /api/
	Register("only_paths", func(options map[string]string) (Filter, error) {
		prefix := options["prefix"]
		return Funcs{
			FilterName: "only_paths",
			Span: func(s model.Span) (model.Span, bool) {
				route, _ := s.Attributes["http.route"].(string)
				return s, prefix == "" || strings.HasPrefix(route, prefix)
			},
		}, nil
	})
}

// Shape 3 — implement Filter directly when the decision needs the whole
// batch: deduplication, rate limiting, cross-record correlation. Guard any
// state with a mutex; batches arrive concurrently.
//
//	type dedupe struct {
//		mu   sync.Mutex
//		seen map[string]time.Time
//	}
//
//	func (d *dedupe) Name() string { return "dedupe" }
//	func (d *dedupe) Spans(in []model.Span) []model.Span   { ... }
//	func (d *dedupe) Metrics(in []model.Metric) []model.Metric { return in }
//	func (d *dedupe) Logs(in []model.Log) []model.Log      { return in }
//
//	func init() {
//		Register("dedupe", func(map[string]string) (Filter, error) {
//			return &dedupe{seen: map[string]time.Time{}}, nil
//		})
//	}
