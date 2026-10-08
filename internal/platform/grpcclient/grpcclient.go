// Package grpcclient dials another service the way a gRPC client on ECS has to
// be dialled.
//
// This file is the fix for the single most common gRPC-on-ECS bug.
//
// By default grpc-go uses the `pick_first` load-balancing policy: it resolves
// the target, connects to ONE address, and sends every RPC over that one
// HTTP/2 connection. So you scale identityd to three tasks, Cloud Map returns
// three A records, and 100% of your traffic still lands on a single task.
// People conclude DNS is broken. DNS is fine; the client simply never asked
// for balancing.
//
// Two settings are required, and they are easy to half-fix:
//
//   - here, the client asks for `round_robin` and uses the dns:/// resolver so
//     it sees every A record rather than just the first;
//   - on the SERVER, grpcserver sets MaxConnectionAge, so connections are
//     recycled and clients re-resolve. Without that, a client that connected
//     before a scale-out never discovers the new tasks, no matter what policy
//     it uses.
package grpcclient

import (
	"fmt"
	"strings"
	"time"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// Options configures the dial.
type Options struct {
	TracerProvider trace.TracerProvider
	// Registry is required: client metrics are useless if unregistered, which
	// was defect #6 in the repo this replaces - the collector was created and
	// then silently never registered, so every client metric was dropped.
	Registry *prometheus.Registry
}

// roundRobin tells grpc-go to spread RPCs across every resolved address.
const roundRobin = `{"loadBalancingConfig":[{"round_robin":{}}]}`

// Dial connects to target, which may be "host:port" or an explicit scheme such
// as "dns:///identityd.ecom.local:50051".
func Dial(target string, opts Options) (*grpc.ClientConn, error) {
	if opts.Registry == nil {
		return nil, fmt.Errorf("grpcclient: a prometheus registry is required")
	}

	clientMetrics := grpcprom.NewClientMetrics()
	opts.Registry.MustRegister(clientMetrics)

	dialOpts := []grpc.DialOption{
		// Plaintext inside the VPC. TLS terminates at the ALB; task-to-task
		// traffic is authenticated at the packet level by the VPC itself.
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(roundRobin),
		grpc.WithUnaryInterceptor(clientMetrics.UnaryClientInterceptor()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                20 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		}),
	}
	if opts.TracerProvider != nil {
		dialOpts = append(dialOpts, grpc.WithStatsHandler(
			otelgrpc.NewClientHandler(otelgrpc.WithTracerProvider(opts.TracerProvider)),
		))
	}

	conn, err := grpc.NewClient(normalizeTarget(target), dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("grpcclient: dial %s: %w", target, err)
	}
	return conn, nil
}

// normalizeTarget defaults to the dns:/// resolver.
//
// This matters more than it looks: with a bare "host:port" grpc-go uses the
// passthrough resolver, which hands the name straight to the dialer and only
// ever yields ONE address - so round_robin has nothing to balance over.
// dns:/// resolves all A records and keeps re-resolving.
func normalizeTarget(target string) string {
	if strings.Contains(target, "://") {
		return target
	}
	return "dns:///" + target
}
