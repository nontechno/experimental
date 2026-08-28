package forwarder

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
)

// httpForwarder posts OTLP protobuf over HTTP, the same bytes the gRPC
// transport would carry.
type httpForwarder struct {
	cfg    Config
	client *http.Client
	base   string
}

func newHTTP(cfg Config) (Exporter, error) {
	base := cfg.Endpoint
	if !strings.Contains(base, "://") {
		scheme := "https://"
		if cfg.Insecure {
			scheme = "http://"
		}
		base = scheme + base
	}
	base = strings.TrimSuffix(base, "/")

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.HasPrefix(base, "https://") {
		tlsCfg, err := clientTLS(cfg)
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig = tlsCfg
	} else if cfg.CAFile != "" || cfg.CertFile != "" {
		return nil, fmt.Errorf("forward TLS files given but endpoint %s is plain http", base)
	} else {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	return &httpForwarder{
		cfg:    cfg,
		base:   base,
		client: &http.Client{Transport: transport, Timeout: cfg.Timeout},
	}, nil
}

func (h *httpForwarder) Name() string { return "forward(http " + h.base + ")" }

func (h *httpForwarder) ExportTraces(ctx context.Context, td ptrace.Traces) error {
	body, err := ptraceotlp.NewExportRequestFromTraces(td).MarshalProto()
	if err != nil {
		return fmt.Errorf("marshal traces: %w", err)
	}
	return h.post(ctx, "traces", "/v1/traces", body)
}

func (h *httpForwarder) ExportMetrics(ctx context.Context, md pmetric.Metrics) error {
	body, err := pmetricotlp.NewExportRequestFromMetrics(md).MarshalProto()
	if err != nil {
		return fmt.Errorf("marshal metrics: %w", err)
	}
	return h.post(ctx, "metrics", "/v1/metrics", body)
}

func (h *httpForwarder) ExportLogs(ctx context.Context, ld plog.Logs) error {
	body, err := plogotlp.NewExportRequestFromLogs(ld).MarshalProto()
	if err != nil {
		return fmt.Errorf("marshal logs: %w", err)
	}
	return h.post(ctx, "logs", "/v1/logs", body)
}

func (h *httpForwarder) post(ctx context.Context, signal, path string, body []byte) error {
	payload := body
	if useGzip(h.cfg) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(body); err != nil {
			return fmt.Errorf("gzip %s: %w", signal, err)
		}
		if err := zw.Close(); err != nil {
			return fmt.Errorf("gzip %s: %w", signal, err)
		}
		payload = buf.Bytes()
	}

	err := retry(h.cfg, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, h.cfg.Timeout)
		defer cancel()

		req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, h.base+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-protobuf")
		if useGzip(h.cfg) {
			req.Header.Set("Content-Encoding", "gzip")
		}
		for k, v := range h.cfg.Headers {
			req.Header.Set(k, v)
		}

		resp, err := h.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		// Drain so the connection can be reused.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
		}
		return nil
	})
	if err == nil {
		return nil
	}
	if h.cfg.FailOpen {
		log.Printf("forward %s to %s failed (continuing): %v", signal, h.base, err)
		return nil
	}
	return fmt.Errorf("forward %s to %s: %w", signal, h.base, err)
}

func (h *httpForwarder) Close() error {
	h.client.CloseIdleConnections()
	return nil
}
