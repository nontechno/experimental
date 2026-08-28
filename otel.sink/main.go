// Command otel.sink is a standalone OTLP endpoint. It accepts traces,
// metrics and logs over OTLP/gRPC and OTLP/HTTP, prints them, optionally
// writes them to JSONL files, and keeps a bounded window of recent
// telemetry that can be browsed over HTTP.
//
// It is a debugging and testing tool, not a production backend: everything
// it retains lives in memory and is lost on restart.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nontechno/experimental/otel.sink/internal/api"
	"github.com/nontechno/experimental/otel.sink/internal/config"
	"github.com/nontechno/experimental/otel.sink/internal/filter"
	"github.com/nontechno/experimental/otel.sink/internal/forwarder"
	"github.com/nontechno/experimental/otel.sink/internal/receiver"
	"github.com/nontechno/experimental/otel.sink/internal/sink"
	"github.com/nontechno/experimental/otel.sink/internal/store"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

// server is anything main needs to start and stop.
type server interface {
	Serve() error
	Shutdown(context.Context) error
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "otel.sink: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("config", "", "path to a YAML config file (optional)")
		grpcAddr    = flag.String("grpc", "", "OTLP/gRPC listen address (overrides config)")
		httpAddr    = flag.String("http", "", "OTLP/HTTP listen address (overrides config)")
		apiAddr     = flag.String("api", "", "query API listen address (overrides config)")
		verbosity   = flag.String("verbosity", "", "console output: none, basic, normal or detailed")
		fileDir     = flag.String("file-dir", "", "write captured telemetry to this directory (enables the file sink)")
		fileFormat  = flag.String("file-format", "", "file sink format: jsonl (default) or json")
		udsPath     = flag.String("uds", "", "stream records to this Unix socket (enables the UDS sink)")
		udsMode     = flag.String("uds-mode", "", "uds mode: listen (default) or dial")
		listFilters = flag.Bool("list-filters", false, "print the registered filters and exit")
		forwardTo   = flag.String("forward", "", "forward every batch to this OTLP endpoint (proxy mode)")
		forwardMode = flag.String("forward-mode", "", "what to forward: raw (default) or filtered")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("otel.sink", version)
		return nil
	}
	if *listFilters {
		fmt.Println("registered filters:")
		for _, name := range filter.Registered() {
			fmt.Println("  " + name)
		}
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *grpcAddr != "" {
		cfg.OTLP.GRPC.Enabled, cfg.OTLP.GRPC.Endpoint = true, *grpcAddr
	}
	if *httpAddr != "" {
		cfg.OTLP.HTTP.Enabled, cfg.OTLP.HTTP.Endpoint = true, *httpAddr
	}
	if *apiAddr != "" {
		cfg.API.Enabled, cfg.API.Endpoint = true, *apiAddr
	}
	if *verbosity != "" {
		cfg.Console.Verbosity = *verbosity
	}
	if *fileDir != "" {
		cfg.File.Enabled, cfg.File.Dir = true, *fileDir
	}
	if *fileFormat != "" {
		cfg.File.Format = *fileFormat
	}
	if *udsPath != "" {
		cfg.UDS.Enabled, cfg.UDS.Path = true, *udsPath
	}
	if *udsMode != "" {
		cfg.UDS.Mode = *udsMode
	}
	if *forwardTo != "" {
		cfg.Forward.Enabled, cfg.Forward.Endpoint = true, *forwardTo
	}
	if *forwardMode != "" {
		cfg.Forward.Mode = *forwardMode
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	// Filters run ahead of every sink, so all outputs see the same records.
	specs := make([]filter.Spec, 0, len(cfg.Filters))
	for _, f := range cfg.Filters {
		specs = append(specs, filter.Spec{Name: f.Name, Options: f.Options})
	}
	chain, err := filter.Build(specs)
	if err != nil {
		return err
	}
	if len(chain) > 0 {
		log.Printf("filters: %s", strings.Join(chain.Names(), " -> "))
	}

	// Sinks. Everything below implements sink.Sink; the Fanout owns their
	// lifecycle, so adding an output means appending to this slice.
	st := store.New(cfg.Store.MaxSpans, cfg.Store.MaxMetrics, cfg.Store.MaxLogs)
	sinks := []sink.Sink{st}
	if cfg.Console.Verbosity != config.VerbosityNone {
		sinks = append(sinks, sink.NewConsole(os.Stdout, cfg.Console.Verbosity))
	}
	if cfg.File.Enabled {
		fs, err := sink.NewFile(cfg.File.Dir, cfg.File.Format)
		if err != nil {
			return err
		}
		sinks = append(sinks, fs)
		log.Printf("writing %s to %s", cfg.File.Format, strings.Join(fs.Paths(), ", "))
	}
	if cfg.UDS.Enabled {
		us, err := sink.NewUDS(cfg.UDS.Path, cfg.UDS.Mode, cfg.UDS.Envelope, cfg.UDS.Buffer)
		if err != nil {
			return err
		}
		sinks = append(sinks, us)
		log.Printf("streaming JSONL to unix socket %s (%s)", cfg.UDS.Path, cfg.UDS.Mode)
	}

	// Exporters forward the batch onward. They take pdata rather than the
	// flattened records, so a proxied batch keeps everything the sender put
	// in it.
	var exporters []sink.Exporter
	if cfg.Forward.Enabled {
		timeout, err := cfg.ForwardTimeout()
		if err != nil {
			return err
		}
		fw, err := forwarder.New(forwarder.Config{
			Endpoint:    cfg.Forward.Endpoint,
			Protocol:    cfg.Forward.Protocol,
			Insecure:    cfg.Forward.Insecure,
			CAFile:      cfg.Forward.CAFile,
			CertFile:    cfg.Forward.CertFile,
			KeyFile:     cfg.Forward.KeyFile,
			Headers:     cfg.Forward.Headers,
			Compression: cfg.Forward.Compression,
			Timeout:     timeout,
			Retries:     cfg.Forward.Retries,
			FailOpen:    cfg.Forward.FailOpen,
		})
		if err != nil {
			return err
		}
		exporters = append(exporters, fw)
		log.Printf("forwarding %s batches to %s over %s (fail_open=%v)",
			cfg.Forward.Mode, cfg.Forward.Endpoint, cfg.Forward.Protocol, cfg.Forward.FailOpen)
		if cfg.Forward.Mode == sink.ForwardRaw && len(chain) > 0 {
			log.Printf("note: forward.mode=raw sends unfiltered batches upstream; " +
				"use mode=filtered to apply the filter chain to forwarded data too")
		}
	}

	consumer := sink.NewFanout(sink.FanoutConfig{
		Filters:     chain,
		Sinks:       sinks,
		Exporters:   exporters,
		ForwardMode: cfg.Forward.Mode,
	})
	// Closing the Fanout closes every sink: it writes the JSON array
	// terminator, drains the socket queue and removes the socket file. A
	// SIGKILL skips it.
	defer func() {
		if err := consumer.Close(); err != nil {
			log.Printf("close sinks: %v", err)
		}
	}()
	log.Printf("outputs: %s", strings.Join(consumer.Names(), ", "))

	// Receivers and API. Listeners bind here so that an address conflict
	// fails immediately instead of half-starting.
	var servers []server
	if cfg.OTLP.GRPC.Enabled {
		g, err := receiver.NewGRPC(cfg.OTLP.GRPC, cfg.MaxRequestBytes(), consumer)
		if err != nil {
			return err
		}
		servers = append(servers, g)
		log.Printf("OTLP/gRPC listening on %s%s", g.Addr(), tlsNote(cfg.OTLP.GRPC))
	}
	if cfg.OTLP.HTTP.Enabled {
		h, err := receiver.NewHTTP(cfg.OTLP.HTTP, int64(cfg.MaxRequestBytes()), consumer)
		if err != nil {
			return err
		}
		servers = append(servers, h)
		log.Printf("OTLP/HTTP listening on %s%s (/v1/traces, /v1/metrics, /v1/logs)", h.Addr(), tlsNote(cfg.OTLP.HTTP))
	}
	if cfg.API.Enabled {
		a, err := api.New(cfg.API.Endpoint, st)
		if err != nil {
			return err
		}
		servers = append(servers, a)
		log.Printf("dashboard and query API on http://%s", a.Addr())
	}

	errCh := make(chan error, len(servers))
	for _, s := range servers {
		go func(s server) { errCh <- s.Serve() }(s)
	}

	// Wait for a signal or for any server to fail.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	var runErr error
	select {
	case sig := <-sigCh:
		log.Printf("received %s, shutting down", sig)
	case runErr = <-errCh:
		if runErr != nil {
			log.Printf("server failed: %v", runErr)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, s := range servers {
		if err := s.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}

	stats := st.Stats()
	log.Printf("captured %d spans, %d metric streams, %d log records",
		stats.SpansReceived, stats.MetricsReceived, stats.LogsReceived)
	if fs, fm, fl := consumer.Filtered(); fs+fm+fl > 0 {
		log.Printf("filters dropped %d spans, %d metric streams, %d log records", fs, fm, fl)
	}
	return runErr
}

func tlsNote(ep config.Endpoint) string {
	if !ep.TLS.Enabled {
		return " (plaintext)"
	}
	if ep.TLS.ClientCAFile != "" {
		return " (mTLS)"
	}
	return " (TLS)"
}
