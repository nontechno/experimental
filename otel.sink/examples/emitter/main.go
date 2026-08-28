// Command emitter is a small instrumented app used to exercise otel.sink.
// It emits a trace with nested spans, a counter and a histogram, and a few
// log records, once per interval, over OTLP/gRPC.
//
// Run otel.sink first, then:
//
//	cd examples/emitter && go mod tidy && go run . -endpoint localhost:4317
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

func main() {
	endpoint := flag.String("endpoint", "localhost:4317", "OTLP/gRPC endpoint of the collector")
	service := flag.String("service", "checkout", "service.name to report")
	interval := flag.Duration("interval", 2*time.Second, "time between iterations")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdown, err := setup(ctx, *endpoint, *service)
	if err != nil {
		log.Fatalf("setup telemetry: %v", err)
	}
	defer func() {
		// Give the exporters a moment to flush the final batch.
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(flushCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()

	tracer := otel.Tracer("emitter")
	meter := otel.Meter("emitter")
	logger := otelslog.NewLogger("emitter")

	orders, err := meter.Int64Counter("orders.processed",
		metric.WithDescription("Orders processed"), metric.WithUnit("{order}"))
	if err != nil {
		log.Fatal(err)
	}
	latency, err := meter.Float64Histogram("checkout.duration",
		metric.WithDescription("Checkout handler duration"), metric.WithUnit("ms"))
	if err != nil {
		log.Fatal(err)
	}
	inFlight, err := meter.Int64UpDownCounter("checkout.in_flight",
		metric.WithDescription("Checkouts currently running"))
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("emitting to %s as %q every %s; press Ctrl-C to stop", *endpoint, *service, *interval)
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for i := 1; ; i++ {
		iterate(ctx, i, tracer, logger, orders, latency, inFlight)
		select {
		case <-ctx.Done():
			log.Print("stopping")
			return
		case <-ticker.C:
		}
	}
}

func iterate(
	ctx context.Context,
	n int,
	tracer trace.Tracer,
	logger *slog.Logger,
	orders metric.Int64Counter,
	latency metric.Float64Histogram,
	inFlight metric.Int64UpDownCounter,
) {
	start := time.Now()
	ctx, span := tracer.Start(ctx, "POST /checkout")
	defer span.End()

	inFlight.Add(ctx, 1)
	defer inFlight.Add(ctx, -1)

	region := attribute.String("region", "us-west-2")
	span.SetAttributes(region, attribute.Int("order.id", 1000+n))
	logger.InfoContext(ctx, "checkout started", "order.id", 1000+n)

	// A child span for a fake database call.
	work(ctx, tracer, "db.query", 15*time.Millisecond)
	// A child span for a fake payment call, which occasionally fails.
	failed := n%5 == 0
	_, payment := tracer.Start(ctx, "payment.authorize")
	time.Sleep(time.Duration(20+rand.Intn(40)) * time.Millisecond)
	if failed {
		err := errors.New("card issuer declined")
		payment.RecordError(err)
		payment.SetStatus(codes.Error, err.Error())
		span.SetStatus(codes.Error, "checkout failed")
		logger.ErrorContext(ctx, "payment declined", "order.id", 1000+n, "error", err)
	}
	payment.AddEvent("authorization.result", trace.WithAttributes(
		attribute.Bool("approved", !failed)))
	payment.End()

	status := attribute.String("status", "ok")
	if failed {
		status = attribute.String("status", "declined")
	}
	orders.Add(ctx, 1, metric.WithAttributes(status, region))
	latency.Record(ctx, float64(time.Since(start).Microseconds())/1000.0,
		metric.WithAttributes(region))
	logger.InfoContext(ctx, "checkout finished",
		"order.id", 1000+n, "duration_ms", time.Since(start).Milliseconds())
}

func work(ctx context.Context, tracer trace.Tracer, name string, d time.Duration) {
	_, span := tracer.Start(ctx, name)
	defer span.End()
	time.Sleep(d + time.Duration(rand.Intn(10))*time.Millisecond)
}

// setup wires the three SDK providers to OTLP/gRPC exporters and returns a
// single shutdown func that flushes all of them.
func setup(ctx context.Context, endpoint, service string) (func(context.Context) error, error) {
	// resource.Default() carries the schema URL of whichever semconv version
	// the installed SDK was built against. Declaring our own here (i.e.
	// passing semconv.SchemaURL) makes Merge fail with "conflicting Schema
	// URL" whenever the two differ, which happens on any SDK upgrade. An
	// empty schema URL merges cleanly and adopts the SDK's.
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		"",
		semconv.ServiceName(service),
		semconv.ServiceVersion("1.0.0"),
		// v1.26.0 spells this deployment.environment; semconv v1.27.0
		// renamed it to deployment.environment.name.
		semconv.DeploymentEnvironment("local"),
	))
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	traceExp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp, sdktrace.WithBatchTimeout(time.Second)),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	metricExp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint), otlpmetricgrpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(5*time.Second))),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	logExp, err := otlploggrpc.New(ctx,
		otlploggrpc.WithEndpoint(endpoint), otlploggrpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)
	global.SetLoggerProvider(lp)

	return func(ctx context.Context) error {
		return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx), lp.Shutdown(ctx))
	}, nil
}
