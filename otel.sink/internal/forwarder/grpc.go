package forwarder

import (
	"context"
	"fmt"
	"log"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	_ "google.golang.org/grpc/encoding/gzip" // registers the gzip compressor
	"google.golang.org/grpc/metadata"
)

// grpcForwarder speaks OTLP/gRPC using the client stubs that ship with
// pdata, so forwarding needs no exporter library.
type grpcForwarder struct {
	cfg    Config
	conn   *grpc.ClientConn
	traces ptraceotlp.GRPCClient
	metric pmetricotlp.GRPCClient
	logs   plogotlp.GRPCClient
}

func newGRPC(cfg Config) (Exporter, error) {
	opts := []grpc.DialOption{}

	if cfg.Insecure {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		tlsCfg, err := clientTLS(cfg)
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	}
	if useGzip(cfg) {
		opts = append(opts, grpc.WithDefaultCallOptions(grpc.UseCompressor("gzip")))
	}

	// NewClient connects lazily, so a collector that is not up yet does not
	// stop otel.sink from starting.
	conn, err := grpc.NewClient(cfg.Endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("forward to %s: %w", cfg.Endpoint, err)
	}
	return &grpcForwarder{
		cfg:    cfg,
		conn:   conn,
		traces: ptraceotlp.NewGRPCClient(conn),
		metric: pmetricotlp.NewGRPCClient(conn),
		logs:   plogotlp.NewGRPCClient(conn),
	}, nil
}

func (g *grpcForwarder) Name() string { return "forward(grpc " + g.cfg.Endpoint + ")" }

func (g *grpcForwarder) ExportTraces(ctx context.Context, td ptrace.Traces) error {
	req := ptraceotlp.NewExportRequestFromTraces(td)
	return g.send(ctx, "traces", func(ctx context.Context) error {
		_, err := g.traces.Export(ctx, req)
		return err
	})
}

func (g *grpcForwarder) ExportMetrics(ctx context.Context, md pmetric.Metrics) error {
	req := pmetricotlp.NewExportRequestFromMetrics(md)
	return g.send(ctx, "metrics", func(ctx context.Context) error {
		_, err := g.metric.Export(ctx, req)
		return err
	})
}

func (g *grpcForwarder) ExportLogs(ctx context.Context, ld plog.Logs) error {
	req := plogotlp.NewExportRequestFromLogs(ld)
	return g.send(ctx, "logs", func(ctx context.Context) error {
		_, err := g.logs.Export(ctx, req)
		return err
	})
}

// send applies headers, timeout, retries and the fail-open policy.
func (g *grpcForwarder) send(ctx context.Context, signal string, call func(context.Context) error) error {
	if len(g.cfg.Headers) > 0 {
		ctx = metadata.NewOutgoingContext(ctx, metadata.New(g.cfg.Headers))
	}
	err := retry(g.cfg, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, g.cfg.Timeout)
		defer cancel()
		return call(attemptCtx)
	})
	if err == nil {
		return nil
	}
	if g.cfg.FailOpen {
		log.Printf("forward %s to %s failed (continuing): %v", signal, g.cfg.Endpoint, err)
		return nil
	}
	return fmt.Errorf("forward %s to %s: %w", signal, g.cfg.Endpoint, err)
}

func (g *grpcForwarder) Close() error {
	if g.conn == nil {
		return nil
	}
	return g.conn.Close()
}
