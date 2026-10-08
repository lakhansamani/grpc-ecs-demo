// Command healthcheck probes grpc.health.v1 and exits 0 when SERVING.
//
// It exists because the runtime image is distroless: there is no shell, no
// curl and no grpc-health-probe. Rather than add a dependency or a bigger base
// image, this reuses the gRPC client we already compile against.
//
// Used as the ECS task-definition container healthCheck:
//
//	["CMD", "/healthcheck", "-addr", "localhost:50051", "-service", "identityd"]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	addr := flag.String("addr", "localhost:50051", "gRPC address to probe")
	service := flag.String("service", "", "service name registered with the health server")
	timeout := flag.Duration("timeout", 2*time.Second, "probe timeout")
	flag.Parse()

	if err := probe(*addr, *service, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		os.Exit(1)
	}
	fmt.Println("SERVING")
}

func probe(addr, service string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// passthrough: this always probes the local container, so DNS resolution
	// and load balancing would be actively wrong here.
	conn, err := grpc.NewClient("passthrough:///"+addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	resp, err := healthpb.NewHealthClient(conn).Check(ctx,
		&healthpb.HealthCheckRequest{Service: service})
	if err != nil {
		return fmt.Errorf("check: %w", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("status %s", resp.GetStatus())
	}
	return nil
}
