package grpcclient

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// A bare host:port must become dns:/// or round_robin has a single address to
// choose between and the load-balancing fix silently does nothing.
func TestNormalizeTarget(t *testing.T) {
	for in, want := range map[string]string{
		"identityd.ecom.local:50051":        "dns:///identityd.ecom.local:50051",
		"localhost:50051":                   "dns:///localhost:50051",
		"dns:///identityd.ecom.local:50051": "dns:///identityd.ecom.local:50051",
		"passthrough:///127.0.0.1:50051":    "passthrough:///127.0.0.1:50051",
		"unix:///tmp/x.sock":                "unix:///tmp/x.sock",
	} {
		if got := normalizeTarget(in); got != want {
			t.Errorf("normalizeTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDialRequiresRegistry(t *testing.T) {
	if _, err := Dial("localhost:1", Options{}); err == nil {
		t.Fatal("want an error when no registry is supplied")
	}
}

// orderd dials two services from one process. Both register the same gRPC
// client collector names, so without a per-client prefix the second
// MustRegister panics on a duplicate.
func TestTwoClientsShareOneRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()

	if _, err := Dial("user.ecom.local:50051", Options{Registry: reg, Subsystem: "user"}); err != nil {
		t.Fatalf("first dial: %v", err)
	}
	// This is the line that panics if the prefix is dropped in a refactor.
	if _, err := Dial("product.ecom.local:50053", Options{Registry: reg, Subsystem: "product"}); err != nil {
		t.Fatalf("second dial failed: %v", err)
	}
}

// And the proof that the prefix is what prevents the collision: reusing a
// subsystem on the same registry must still panic.
func TestDuplicateSubsystemStillCollides(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := Dial("a:1", Options{Registry: reg, Subsystem: "same"}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("registering the same subsystem twice should collide; " +
				"if this stops panicking, the prefix may no longer be applied at all")
		}
	}()
	_, _ = Dial("b:2", Options{Registry: reg, Subsystem: "same"})
}
