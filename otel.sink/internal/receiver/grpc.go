// Package receiver implements the two OTLP transports: gRPC (port 4317 by
// convention) and HTTP/protobuf or HTTP/JSON (port 4318).
package receiver

import (
	"context"
	"fmt"
	"net"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/nontechno/experimental/otel.sink/internal/config"
	"github.com/nontechno/experimental/otel.sink/internal/sink"
)

// GRPC serves the three OTLP export services on one port.
type GRPC struct {
	server *grpc.Server
	ln     net.Listener
}

// NewGRPC binds the listener and registers the OTLP services. The listener is
// opened here (not in Serve) so that a port conflict fails at startup.
func NewGRPC(cfg config.Endpoint, maxRecvBytes int, c sink.Consumer) (*GRPC, error) {
	opts := []grpc.ServerOption{grpc.MaxRecvMsgSize(maxRecvBytes)}
	if cfg.TLS.Enabled {
		creds, err := serverCredentials(cfg.TLS)
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.Creds(creds))
	}

	srv := grpc.NewServer(opts...)
	ptraceotlp.RegisterGRPCServer(srv, &traceService{c: c})
	pmetricotlp.RegisterGRPCServer(srv, &metricService{c: c})
	plogotlp.RegisterGRPCServer(srv, &logService{c: c})
	// Reflection lets grpcurl introspect the server, which is handy when
	// debugging what a sender is actually calling.
	reflection.Register(srv)

	ln, err := net.Listen("tcp", cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", cfg.Endpoint, err)
	}
	return &GRPC{server: srv, ln: ln}, nil
}

// Addr is the resolved listen address (useful when the port was :0).
func (g *GRPC) Addr() string { return g.ln.Addr().String() }

// Serve blocks until Shutdown is called.
func (g *GRPC) Serve() error {
	if err := g.server.Serve(g.ln); err != nil && err != grpc.ErrServerStopped {
		return err
	}
	return nil
}

// Shutdown stops the server, waiting for in-flight exports to finish.
func (g *GRPC) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		g.server.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		g.server.Stop()
		return ctx.Err()
	}
}

type traceService struct {
	ptraceotlp.UnimplementedGRPCServer
	c sink.Consumer
}

func (s *traceService) Export(ctx context.Context, req ptraceotlp.ExportRequest) (ptraceotlp.ExportResponse, error) {
	if err := s.c.ConsumeTraces(ctx, req.Traces()); err != nil {
		return ptraceotlp.NewExportResponse(), err
	}
	return ptraceotlp.NewExportResponse(), nil
}

type metricService struct {
	pmetricotlp.UnimplementedGRPCServer
	c sink.Consumer
}

func (s *metricService) Export(ctx context.Context, req pmetricotlp.ExportRequest) (pmetricotlp.ExportResponse, error) {
	if err := s.c.ConsumeMetrics(ctx, req.Metrics()); err != nil {
		return pmetricotlp.NewExportResponse(), err
	}
	return pmetricotlp.NewExportResponse(), nil
}

type logService struct {
	plogotlp.UnimplementedGRPCServer
	c sink.Consumer
}

func (s *logService) Export(ctx context.Context, req plogotlp.ExportRequest) (plogotlp.ExportResponse, error) {
	if err := s.c.ConsumeLogs(ctx, req.Logs()); err != nil {
		return plogotlp.NewExportResponse(), err
	}
	return plogotlp.NewExportResponse(), nil
}
