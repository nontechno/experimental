package receiver

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"

	"github.com/nontechno/experimental/otel.sink/internal/config"
	"github.com/nontechno/experimental/otel.sink/internal/sink"
)

const (
	protobufContentType = "application/x-protobuf"
	jsonContentType     = "application/json"
)

// HTTP serves the OTLP/HTTP endpoints: /v1/traces, /v1/metrics, /v1/logs.
type HTTP struct {
	server *http.Server
	ln     net.Listener
	tls    bool
}

// NewHTTP binds the listener and wires the three OTLP paths.
func NewHTTP(cfg config.Endpoint, maxBodyBytes int64, c sink.Consumer) (*HTTP, error) {
	h := &handler{c: c, maxBody: maxBodyBytes}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", h.traces)
	mux.HandleFunc("/v1/metrics", h.metrics)
	mux.HandleFunc("/v1/logs", h.logs)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := net.Listen("tcp", cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", cfg.Endpoint, err)
	}
	out := &HTTP{server: srv, ln: ln, tls: cfg.TLS.Enabled}
	if cfg.TLS.Enabled {
		tlsCfg, err := serverTLSConfig(cfg.TLS)
		if err != nil {
			_ = ln.Close()
			return nil, err
		}
		srv.TLSConfig = tlsCfg
	}
	return out, nil
}

// Addr is the resolved listen address.
func (h *HTTP) Addr() string { return h.ln.Addr().String() }

// Serve blocks until Shutdown is called.
func (h *HTTP) Serve() error {
	var err error
	if h.tls {
		// Certificates are already in server.TLSConfig.
		err = h.server.ServeTLS(h.ln, "", "")
	} else {
		err = h.server.Serve(h.ln)
	}
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown drains in-flight requests.
func (h *HTTP) Shutdown(ctx context.Context) error { return h.server.Shutdown(ctx) }

type handler struct {
	c       sink.Consumer
	maxBody int64
}

func (h *handler) traces(w http.ResponseWriter, r *http.Request) {
	body, enc, ok := h.readBody(w, r)
	if !ok {
		return
	}
	req := ptraceotlp.NewExportRequest()
	if err := unmarshal(&req, body, enc); err != nil {
		httpError(w, http.StatusBadRequest, "decode trace request: %v", err)
		return
	}
	if err := h.c.ConsumeTraces(r.Context(), req.Traces()); err != nil {
		httpError(w, http.StatusInternalServerError, "consume traces: %v", err)
		return
	}
	resp := ptraceotlp.NewExportResponse()
	writeResponse(w, enc, resp.MarshalProto, resp.MarshalJSON)
}

func (h *handler) metrics(w http.ResponseWriter, r *http.Request) {
	body, enc, ok := h.readBody(w, r)
	if !ok {
		return
	}
	req := pmetricotlp.NewExportRequest()
	if err := unmarshal(&req, body, enc); err != nil {
		httpError(w, http.StatusBadRequest, "decode metric request: %v", err)
		return
	}
	if err := h.c.ConsumeMetrics(r.Context(), req.Metrics()); err != nil {
		httpError(w, http.StatusInternalServerError, "consume metrics: %v", err)
		return
	}
	resp := pmetricotlp.NewExportResponse()
	writeResponse(w, enc, resp.MarshalProto, resp.MarshalJSON)
}

func (h *handler) logs(w http.ResponseWriter, r *http.Request) {
	body, enc, ok := h.readBody(w, r)
	if !ok {
		return
	}
	req := plogotlp.NewExportRequest()
	if err := unmarshal(&req, body, enc); err != nil {
		httpError(w, http.StatusBadRequest, "decode log request: %v", err)
		return
	}
	if err := h.c.ConsumeLogs(r.Context(), req.Logs()); err != nil {
		httpError(w, http.StatusInternalServerError, "consume logs: %v", err)
		return
	}
	resp := plogotlp.NewExportResponse()
	writeResponse(w, enc, resp.MarshalProto, resp.MarshalJSON)
}

// unmarshaler is satisfied by all three otlp ExportRequest types.
type unmarshaler interface {
	UnmarshalProto([]byte) error
	UnmarshalJSON([]byte) error
}

func unmarshal(req unmarshaler, body []byte, enc string) error {
	if enc == jsonContentType {
		return req.UnmarshalJSON(body)
	}
	return req.UnmarshalProto(body)
}

// readBody validates the method and content type, decompresses gzip, and
// enforces the size limit. The returned string is the negotiated encoding.
func (h *handler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, string, bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpError(w, http.StatusMethodNotAllowed, "use POST")
		return nil, "", false
	}

	enc := protobufContentType
	switch ct := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); ct {
	case protobufContentType, "application/protobuf", "":
		enc = protobufContentType
	case jsonContentType:
		enc = jsonContentType
	default:
		httpError(w, http.StatusUnsupportedMediaType,
			"content type %q: want %s or %s", ct, protobufContentType, jsonContentType)
		return nil, "", false
	}

	var reader io.Reader = http.MaxBytesReader(w, r.Body, h.maxBody)
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			httpError(w, http.StatusBadRequest, "gzip: %v", err)
			return nil, "", false
		}
		defer gz.Close()
		reader = gz
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		httpError(w, http.StatusRequestEntityTooLarge, "read body: %v", err)
		return nil, "", false
	}
	return body, enc, true
}

// writeResponse serialises the (empty) OTLP success response in the same
// encoding the client used, which is what the spec requires.
func writeResponse(w http.ResponseWriter, enc string, marshalProto, marshalJSON func() ([]byte, error)) {
	var (
		b   []byte
		err error
	)
	if enc == jsonContentType {
		b, err = marshalJSON()
	} else {
		b, err = marshalProto()
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, "encode response: %v", err)
		return
	}
	w.Header().Set("Content-Type", enc)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func httpError(w http.ResponseWriter, code int, format string, args ...any) {
	http.Error(w, fmt.Sprintf(format, args...), code)
}
