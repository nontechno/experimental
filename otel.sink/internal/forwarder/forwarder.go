// Package forwarder re-exports received OTLP batches to another OTLP
// endpoint, which is what makes otel.sink usable as a proxy: capture
// locally and pass everything on to a real backend.
//
// Batches are forwarded as pdata, not rebuilt from otel.sink's flattened
// records, so nothing is lost in translation — span links, exponential
// histogram bucket layouts, dropped-attribute counts and schema URLs all
// survive.
//
// No new dependencies: the OTLP client stubs ship with pdata, and the HTTP
// transport is net/http posting the same protobuf bytes.
package forwarder

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// Transport protocols.
const (
	ProtocolGRPC = "grpc"
	ProtocolHTTP = "http"
)

// Config describes the upstream endpoint.
type Config struct {
	// Endpoint is host:port for grpc, or a base URL for http. For http the
	// signal paths (/v1/traces and friends) are appended.
	Endpoint string
	Protocol string
	// Insecure sends plaintext. Ignored when the http endpoint already
	// carries an https:// scheme.
	Insecure bool
	// CAFile verifies the upstream certificate; CertFile and KeyFile add a
	// client certificate for mTLS.
	CAFile   string
	CertFile string
	KeyFile  string
	// Headers are attached to every request: auth tokens, tenant IDs.
	Headers map[string]string
	// Compression is "gzip" or empty.
	Compression string
	Timeout     time.Duration
	// Retries is the number of extra attempts after a failure.
	Retries int
	// FailOpen logs export failures and reports success to the sender, so a
	// dead upstream does not break local capture. With it off, the failure
	// propagates back to the sending SDK, which then applies its own retry
	// and drop policy — the correct behaviour for a pure proxy.
	FailOpen bool
}

// Exporter is one upstream connection. It matches the sink.Exporter
// interface that the Fanout expects.
type Exporter interface {
	Name() string
	ExportTraces(context.Context, ptrace.Traces) error
	ExportMetrics(context.Context, pmetric.Metrics) error
	ExportLogs(context.Context, plog.Logs) error
	Close() error
}

// New builds a forwarder for the configured protocol.
func New(cfg Config) (Exporter, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("forward.endpoint is empty")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Retries < 0 {
		cfg.Retries = 0
	}
	switch cfg.Compression {
	case "", "none", "gzip":
	default:
		return nil, fmt.Errorf("forward.compression %q: want none or gzip", cfg.Compression)
	}

	switch cfg.Protocol {
	case ProtocolGRPC, "":
		cfg.Protocol = ProtocolGRPC
		return newGRPC(cfg)
	case ProtocolHTTP, "http/protobuf":
		cfg.Protocol = ProtocolHTTP
		return newHTTP(cfg)
	default:
		return nil, fmt.Errorf("forward.protocol %q: want %s or %s", cfg.Protocol, ProtocolGRPC, ProtocolHTTP)
	}
}

// clientTLS builds the client-side TLS configuration.
func clientTLS(cfg Config) (*tls.Config, error) {
	out := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read forward CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("forward CA %s contains no certificates", cfg.CAFile)
		}
		out.RootCAs = pool
	}
	if cfg.CertFile != "" || cfg.KeyFile != "" {
		if cfg.CertFile == "" || cfg.KeyFile == "" {
			return nil, fmt.Errorf("forward mTLS needs both cert_file and key_file")
		}
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load forward client key pair: %w", err)
		}
		out.Certificates = []tls.Certificate{cert}
	}
	return out, nil
}

// retry runs attempt up to 1+Retries times with a short linear backoff.
// Export failures here are usually an upstream restart or a transient
// network error, both of which clear quickly.
func retry(cfg Config, attempt func() error) error {
	var err error
	for i := 0; i <= cfg.Retries; i++ {
		if i > 0 {
			time.Sleep(time.Duration(i) * 250 * time.Millisecond)
		}
		if err = attempt(); err == nil {
			return nil
		}
	}
	return err
}

// useGzip reports whether the payload should be compressed.
func useGzip(cfg Config) bool {
	return strings.EqualFold(cfg.Compression, "gzip")
}
