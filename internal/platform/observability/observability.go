// Package observability wires OpenTelemetry tracing.
//
// Tracing is OPTIONAL by design: if OTEL_EXPORTER_OTLP_ENDPOINT is unset the
// service runs with a no-op provider instead of refusing to start. A collector
// being down must never take the service down with it.
package observability

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Config describes the tracer.
type Config struct {
	ServiceName string
	Version     string
	Environment string
	// OTLPEndpoint is host:port for the gRPC exporter. Empty disables tracing.
	OTLPEndpoint string
}

// Shutdown flushes pending spans. Always call it, and always check the error -
// dropping it means losing the last batch of traces on every exit.
type Shutdown func(context.Context) error

// Init returns a TracerProvider and its shutdown function.
func Init(ctx context.Context, cfg Config) (trace.TracerProvider, Shutdown, error) {
	if cfg.OTLPEndpoint == "" {
		return noop.NewTracerProvider(), func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("observability: otlp exporter: %w", err)
	}

	// The semconv version MUST match the one the SDK's resource.Default()
	// uses, or Merge fails with "conflicting Schema URL" and the service
	// refuses to start. That only shows up once an OTLP endpoint is actually
	// configured, so it hides from any local run that leaves tracing off -
	// which is exactly why this got deployed before the talk and not during it.
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(cfg.Version),
		semconv.DeploymentEnvironmentNameKey.String(cfg.Environment),
	))
	if err != nil {
		return nil, nil, fmt.Errorf("observability: resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	// Without a propagator, the trace context never crosses the service
	// boundary and paymentd -> identityd shows up as two unrelated traces.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	return tp, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return tp.Shutdown(ctx)
	}, nil
}
