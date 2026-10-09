package grpcserver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	userv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/user/v1"
)

// A hand-rolled service with a deliberately slow method, so we can start an
// RPC, trigger shutdown underneath it, and prove the drain waits.
const (
	slowService = "test.Slow"
	slowMethod  = "Wait"
)

func slowDesc(delay time.Duration) *grpc.ServiceDesc {
	return &grpc.ServiceDesc{
		ServiceName: slowService,
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: slowMethod,
			Handler: func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				var req userv1.VerifyTokenRequest
				if err := dec(&req); err != nil {
					return nil, err
				}
				time.Sleep(delay)
				return &userv1.VerifyTokenResponse{
					User: &userv1.User{Id: "drained"},
				}, nil
			},
		}},
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func startServer(t *testing.T, delay time.Duration) (addr string, cancel func(), done <-chan error) {
	t.Helper()
	grpcAddr, metricsAddr := freeAddr(t), freeAddr(t)

	s := New(Config{
		ServiceName:     "testd",
		GRPCAddr:        grpcAddr,
		MetricsAddr:     metricsAddr,
		ShutdownTimeout: 10 * time.Second,
		Registry:        prometheus.NewRegistry(),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.GRPC().RegisterService(slowDesc(delay), nil)

	ctx, stop := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Serve(ctx) }()

	// Wait until it is actually accepting connections.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", grpcAddr, 200*time.Millisecond); err == nil {
			_ = c.Close()
			return grpcAddr, stop, errc
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	t.Fatal("server never started listening")
	return "", nil, nil
}

func dial(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return cc
}

func TestHealthReportsServing(t *testing.T) {
	addr, stop, done := startServer(t, 0)
	defer func() { stop(); <-done }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := healthpb.NewHealthClient(dial(t, addr)).
		Check(ctx, &healthpb.HealthCheckRequest{Service: "testd"})
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING", resp.GetStatus())
	}
}

// The one that matters: an RPC in flight when SIGTERM arrives must still finish.
// Without GracefulStop this fails with "transport is closing" - which is
// exactly what a deploy does to live traffic in the repo this demo replaces.
func TestInFlightRPCSurvivesShutdown(t *testing.T) {
	const handlerDelay = 700 * time.Millisecond
	addr, stop, done := startServer(t, handlerDelay)
	cc := dial(t, addr)

	// Pointer, not value: a proto message carries a mutex and must not be copied.
	type result struct {
		resp *userv1.VerifyTokenResponse
		err  error
	}
	res := make(chan result, 1)
	go func() {
		out := &userv1.VerifyTokenResponse{}
		err := cc.Invoke(context.Background(),
			"/"+slowService+"/"+slowMethod,
			&userv1.VerifyTokenRequest{}, out)
		res <- result{out, err}
	}()

	// Let the call reach the handler, then pull the rug out.
	time.Sleep(150 * time.Millisecond)
	stop()

	select {
	case r := <-res:
		if r.err != nil {
			t.Fatalf("in-flight RPC was killed by shutdown: %v", r.err)
		}
		if got := r.resp.GetUser().GetId(); got != "drained" {
			t.Fatalf("user id = %q, want %q", got, "drained")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight RPC never returned")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil on clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after shutdown")
	}
}

// A drain that outlives its deadline must not hang forever; ECS will SIGKILL.
func TestShutdownTimeoutForcesStop(t *testing.T) {
	grpcAddr, metricsAddr := freeAddr(t), freeAddr(t)
	s := New(Config{
		ServiceName:     "testd",
		GRPCAddr:        grpcAddr,
		MetricsAddr:     metricsAddr,
		ShutdownTimeout: 200 * time.Millisecond, // shorter than the handler
		Registry:        prometheus.NewRegistry(),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.GRPC().RegisterService(slowDesc(5*time.Second), nil)

	ctx, stop := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Serve(ctx) }()

	for i := 0; i < 250; i++ {
		if c, err := net.DialTimeout("tcp", grpcAddr, 200*time.Millisecond); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cc := dial(t, grpcAddr)
	go func() {
		out := &userv1.VerifyTokenResponse{}
		_ = cc.Invoke(context.Background(), "/"+slowService+"/"+slowMethod,
			&userv1.VerifyTokenRequest{}, out)
	}()
	time.Sleep(150 * time.Millisecond)
	stop()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve hung past its shutdown timeout")
	}
}
