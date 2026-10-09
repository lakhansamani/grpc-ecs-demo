package grpcclient

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

// A bare host:port must become dns:/// or round_robin has a single address to
// choose between and the load-balancing fix silently does nothing.
func TestNormalizeTarget(t *testing.T) {
	for in, want := range map[string]string{
		"userd.ecom.local:50051":         "dns:///userd.ecom.local:50051",
		"localhost:50051":                "dns:///localhost:50051",
		"dns:///userd.ecom.local:50051":  "dns:///userd.ecom.local:50051",
		"passthrough:///127.0.0.1:50051": "passthrough:///127.0.0.1:50051",
		"unix:///tmp/x.sock":             "unix:///tmp/x.sock",
	} {
		if got := normalizeTarget(in); got != want {
			t.Errorf("normalizeTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

// The demo toggle must break BOTH halves. If it left dns:/// in place the
// "before" state would still resolve every task and the stage demo would show
// nothing going wrong.
func TestBrokenLBTogglePassesTargetThrough(t *testing.T) {
	t.Setenv("LB_POLICY", "pick_first")
	if got := normalizeTarget("userd.ecom.local:50051"); got != "userd.ecom.local:50051" {
		t.Errorf("with LB_POLICY=pick_first, normalizeTarget returned %q, want it untouched", got)
	}
	if !brokenLB() {
		t.Error("brokenLB() should be true when LB_POLICY=pick_first")
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

// Warm must give up rather than block forever when nothing is listening -
// a service whose upstream is down should still start and serve.
func TestWarmTimesOutOnDeadUpstream(t *testing.T) {
	conn, err := Dial("127.0.0.1:1", Options{Registry: prometheus.NewRegistry(), Subsystem: "dead"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	start := time.Now()
	if err := Warm(context.Background(), conn, 300*time.Millisecond); err == nil {
		t.Fatal("want an error for an upstream that is not listening")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Warm took %s; it must respect its timeout", elapsed)
	}
}

// And it must report ready against something that IS listening, so the first
// real RPC does not pay for the handshake.
func TestWarmSucceedsAgainstLiveServer(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	conn, err := Dial(lis.Addr().String(), Options{Registry: prometheus.NewRegistry(), Subsystem: "live"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	if err := Warm(context.Background(), conn, 5*time.Second); err != nil {
		t.Fatalf("Warm against a live server: %v", err)
	}
	if got := conn.GetState(); got != connectivity.Ready {
		t.Fatalf("state after Warm = %v, want Ready", got)
	}
}
