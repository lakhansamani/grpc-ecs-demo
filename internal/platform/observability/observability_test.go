package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace/noop"
)

// Regression test. resource.Merge rejects a resource whose schema URL differs
// from resource.Default()'s, and the failure is invisible unless an OTLP
// endpoint is set - so a local run with tracing off will not catch it. A
// crash-looping ECS task will.
func TestInitWithEndpointBuildsResource(t *testing.T) {
	// The exporter connects lazily, so an unreachable endpoint is fine here:
	// this exercises resource construction, which is what breaks.
	tp, shutdown, err := Init(context.Background(), Config{
		ServiceName:  "testd",
		Version:      "test",
		Environment:  "test",
		OTLPEndpoint: "127.0.0.1:4317",
	})
	if err != nil {
		t.Fatalf("Init with an endpoint failed: %v", err)
	}
	if tp == nil {
		t.Fatal("nil tracer provider")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Logf("shutdown returned %v (acceptable: nothing is listening)", err)
	}
}

func TestInitWithoutEndpointIsNoop(t *testing.T) {
	tp, shutdown, err := Init(context.Background(), Config{ServiceName: "testd"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tp.(noop.TracerProvider); !ok {
		t.Fatalf("want a noop provider when no endpoint is set, got %T", tp)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown should not error: %v", err)
	}
}
