// Package api serves the read-only query API and the browser dashboard.
package api

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nontechno/experimental/otel.sink/internal/model"
	"github.com/nontechno/experimental/otel.sink/internal/store"
)

//go:embed ui.html
var dashboard []byte

// Server exposes the store over HTTP.
type Server struct {
	server *http.Server
	ln     net.Listener
}

// New binds the listener and registers the routes.
func New(endpoint string, st *store.Store) (*Server, error) {
	h := &handler{store: st}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.ui)
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /api/stats", h.stats)
	mux.HandleFunc("GET /api/traces", h.traces)
	mux.HandleFunc("GET /api/traces/{id}", h.trace)
	mux.HandleFunc("GET /api/metrics", h.metrics)
	mux.HandleFunc("GET /api/logs", h.logs)
	mux.HandleFunc("GET /api/export", h.export)
	mux.HandleFunc("POST /api/reset", h.reset)

	srv := &http.Server{
		Handler:           cors(mux),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	ln, err := net.Listen("tcp", endpoint)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", endpoint, err)
	}
	return &Server{server: srv, ln: ln}, nil
}

// Addr is the resolved listen address.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Serve blocks until Shutdown is called.
func (s *Server) Serve() error {
	if err := s.server.Serve(s.ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown drains in-flight requests.
func (s *Server) Shutdown(ctx context.Context) error { return s.server.Shutdown(ctx) }

type handler struct {
	store *store.Store
}

func (h *handler) ui(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(dashboard)
}

func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *handler) stats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, h.store.Stats())
}

func (h *handler) traces(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	spans := h.store.QuerySpans(store.SpanQuery{
		Service: q.Get("service"),
		Name:    q.Get("name"),
		TraceID: q.Get("trace_id"),
		Status:  q.Get("status"),
		Limit:   intParam(q.Get("limit"), 100),
	})
	writeJSON(w, map[string]any{"count": len(spans), "spans": spans})
}

func (h *handler) trace(w http.ResponseWriter, r *http.Request) {
	spans := h.store.Trace(r.PathValue("id"))
	if len(spans) == 0 {
		http.Error(w, "trace not found in the retained window", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"trace_id": r.PathValue("id"), "count": len(spans), "spans": spans})
}

func (h *handler) metrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	metrics := h.store.QueryMetrics(store.MetricQuery{
		Service: q.Get("service"),
		Name:    q.Get("name"),
		Type:    q.Get("type"),
		Limit:   intParam(q.Get("limit"), 100),
	})
	writeJSON(w, map[string]any{"count": len(metrics), "metrics": metrics})
}

func (h *handler) logs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	logs := h.store.QueryLogs(store.LogQuery{
		Service:     q.Get("service"),
		Contains:    q.Get("contains"),
		TraceID:     q.Get("trace_id"),
		MinSeverity: int32(intParam(q.Get("min_severity"), 0)),
		Limit:       intParam(q.Get("limit"), 100),
	})
	writeJSON(w, map[string]any{"count": len(logs), "logs": logs})
}

// export returns a downloadable snapshot of what the store currently holds.
// Records come back oldest first, so an exported file reads chronologically
// like the file sink's output, unlike the newest-first browse endpoints.
//
//	GET /api/export?signal=traces|metrics|logs|all&format=json|jsonl
//
// It accepts the same filters as the browse endpoints, and defaults to
// everything held rather than to 100 rows.
func (h *handler) export(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	signal := strings.ToLower(q.Get("signal"))
	if signal == "" {
		signal = "all"
	}
	format := strings.ToLower(q.Get("format"))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "jsonl" {
		http.Error(w, "format must be json or jsonl", http.StatusBadRequest)
		return
	}
	if format == "jsonl" && signal == "all" {
		http.Error(w, "format=jsonl needs signal=traces, metrics or logs: "+
			"one file cannot hold three record types as bare lines", http.StatusBadRequest)
		return
	}
	limit := intParam(q.Get("limit"), 1<<30)

	spans := func() []model.Span {
		return reverse(h.store.QuerySpans(store.SpanQuery{
			Service: q.Get("service"), Name: q.Get("name"),
			TraceID: q.Get("trace_id"), Status: q.Get("status"), Limit: limit,
		}))
	}
	metrics := func() []model.Metric {
		return reverse(h.store.QueryMetrics(store.MetricQuery{
			Service: q.Get("service"), Name: q.Get("name"),
			Type: q.Get("type"), Limit: limit,
		}))
	}
	logs := func() []model.Log {
		return reverse(h.store.QueryLogs(store.LogQuery{
			Service: q.Get("service"), Contains: q.Get("contains"),
			TraceID:     q.Get("trace_id"),
			MinSeverity: int32(intParam(q.Get("min_severity"), 0)), Limit: limit,
		}))
	}

	var (
		buf bytes.Buffer
		err error
	)
	switch signal {
	case "traces":
		err = encodeExport(&buf, format, spans())
	case "metrics":
		err = encodeExport(&buf, format, metrics())
	case "logs":
		err = encodeExport(&buf, format, logs())
	case "all":
		err = encodeExport(&buf, format, map[string]any{
			"exported_at": time.Now().UTC(),
			"stats":       h.store.Stats(),
			"traces":      spans(),
			"metrics":     metrics(),
			"logs":        logs(),
		})
	default:
		http.Error(w, "signal must be traces, metrics, logs or all", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "encode export: "+err.Error(), http.StatusInternalServerError)
		return
	}

	name := fmt.Sprintf("otel.sink-%s-%s.%s", signal, time.Now().UTC().Format("20060102T150405Z"), format)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write(buf.Bytes())
}

// encodeExport writes v as one indented JSON document, or — for a slice in
// jsonl format — as one compact object per line.
func encodeExport(buf *bytes.Buffer, format string, v any) error {
	if format == "json" {
		enc := json.NewEncoder(buf)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	enc := json.NewEncoder(buf)
	switch records := v.(type) {
	case []model.Span:
		return encodeLines(enc, records)
	case []model.Metric:
		return encodeLines(enc, records)
	case []model.Log:
		return encodeLines(enc, records)
	default:
		return fmt.Errorf("cannot write %T as jsonl", v)
	}
}

func encodeLines[T any](enc *json.Encoder, records []T) error {
	for i := range records {
		if err := enc.Encode(records[i]); err != nil {
			return err
		}
	}
	return nil
}

// reverse flips newest-first query results into chronological order.
func reverse[T any](s []T) []T {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	return s
}

func (h *handler) reset(w http.ResponseWriter, _ *http.Request) {
	h.store.Reset()
	writeJSON(w, map[string]string{"status": "cleared"})
}

// writeJSON marshals into a buffer before touching the ResponseWriter.
// Encoding straight to w would emit a 200 plus a half-written document if
// marshalling failed partway, which reaches the client as malformed JSON
// rather than as an error.
func writeJSON(w http.ResponseWriter, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		http.Error(w, "encode response: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func intParam(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

// cors keeps the API usable from a page served elsewhere. The API is
// read-only apart from /api/reset, and is meant for local or trusted
// networks only.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
