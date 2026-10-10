// Package grpcclient dials another service the way a gRPC client on ECS has to
// be dialled.
//
// This file is the fix for the single most common gRPC-on-ECS bug.
//
// By default grpc-go uses the `pick_first` load-balancing policy: it resolves
// the target, connects to ONE address, and sends every RPC over that one
// HTTP/2 connection. So you scale userd to three tasks, Cloud Map returns
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
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
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

	// Subsystem names this client in the metric name. Required when one
	// process dials more than one service (orderd dials two), or the second
	// MustRegister panics on a duplicate collector.
	Subsystem string
}

// roundRobin tells grpc-go to spread RPCs across every resolved address.
const roundRobin = `{"loadBalancingConfig":[{"round_robin":{}}]}`

// brokenLB exists so the bug can be DEMONSTRATED, not just described. Set
// LB_POLICY=pick_first and the client reverts to grpc-go's defaults: no
// service config, and no dns:/// prefix either - because fixing only one of
// the two looks like it works and does not. Anything else is the fix.
func brokenLB() bool { return os.Getenv("LB_POLICY") == "pick_first" }

// Dial connects to target, which may be "host:port" or an explicit scheme such
// as "dns:///userd.ecom.local:50051".
func Dial(target string, opts Options) (*grpc.ClientConn, error) {
	if opts.Registry == nil {
		return nil, fmt.Errorf("grpcclient: a prometheus registry is required")
	}

	clientMetrics := grpcprom.NewClientMetrics()

	// One process may dial several services (orderd dials two), and each
	// outbound client registers the same collector names. Prefix them per
	// client, or the second MustRegister panics on a duplicate.
	reg := prometheus.Registerer(opts.Registry)
	if opts.Subsystem != "" {
		reg = prometheus.WrapRegistererWithPrefix(opts.Subsystem+"_", opts.Registry)
	}
	reg.MustRegister(clientMetrics)

	dialOpts := []grpc.DialOption{
		// Plaintext inside the VPC. TLS terminates at the ALB; task-to-task
		// traffic is authenticated at the packet level by the VPC itself.
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(clientMetrics.UnaryClientInterceptor()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                20 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		}),
	}
	if !brokenLB() {
		dialOpts = append(dialOpts, grpc.WithDefaultServiceConfig(roundRobin))
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
	if brokenLB() {
		return target // passthrough resolver: exactly one address, ever
	}
	return "dns:///" + target
}

// Warm forces the connection to be established before the caller starts
// serving, and waits until it is usable.
//
// Why this is needed: grpc.NewClient is LAZY. It validates the target and
// returns immediately without connecting, so the FIRST RPC pays for name
// resolution and the TCP/HTTP2 handshake - and if the upstream is not up yet,
// that first RPC fails fast rather than waiting. On ECS that means the first
// real request after a deploy can fail even though everything is healthy
// seconds later.
//
// A failure here is not fatal: the upstream may legitimately start after us.
// The caller logs it and serves anyway — but see KeepWarm, because "gRPC
// reconnects on its own" turned out not to be reliably true.
func Warm(ctx context.Context, conn *grpc.ClientConn, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn.Connect()
	for {
		switch s := conn.GetState(); s {
		case connectivity.Ready:
			return nil
		case connectivity.Shutdown:
			return fmt.Errorf("grpcclient: connection to %s is shut down", conn.Target())
		case connectivity.TransientFailure:
			// Usually means the upstream has not registered in service
			// discovery yet. Retry immediately instead of waiting out a
			// backoff that grows towards minutes.
			conn.ResetConnectBackoff()
			if !conn.WaitForStateChange(ctx, s) {
				return fmt.Errorf("grpcclient: %s not ready within %s (state %s)",
					conn.Target(), timeout, s)
			}
		default:
			if !conn.WaitForStateChange(ctx, s) {
				return fmt.Errorf("grpcclient: %s not ready within %s (state %s)",
					conn.Target(), timeout, s)
			}
		}
	}
}

// KeepWarm keeps nudging a connection back towards Ready, forever, in the
// background. Start one per client and forget about it.
//
// WHY THIS EXISTS — a bug found on real AWS, not locally:
//
// On ECS all four services are created at once, so orderd can start BEFORE
// userd has registered itself in Cloud Map. orderd's first DNS lookup for
// userd.ecom.local then returns nothing, the round_robin balancer ends up with
// an empty address list, and every RPC fails with:
//
//	rpc error: code = Unavailable desc = no children to pick from
//
// Everything else looked perfect while that was happening: four tasks RUNNING
// and HEALTHY, four Cloud Map instances HEALTHY, the Route 53 A records present
// and correct, and the security group allowing task-to-task traffic on every
// port. Redeploying orderd — so it booted after userd was registered — fixed it
// immediately, which is what identified the cause.
//
// ResetConnectBackoff is the primitive that matters here: it wakes subchannels
// that are in TRANSIENT_FAILURE and makes them retry at once, instead of
// waiting out a backoff that grows to minutes. Connect covers the IDLE case.
// ResolveNow would be the obvious call, but it is not exported on
// *grpc.ClientConn.
func KeepWarm(ctx context.Context, conn *grpc.ClientConn, every time.Duration) {
	if every <= 0 {
		every = 5 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			switch conn.GetState() {
			case connectivity.Ready:
				// nothing to do
			case connectivity.Shutdown:
				return
			case connectivity.Idle:
				conn.Connect()
			default:
				// CONNECTING or TRANSIENT_FAILURE. Retry now rather than
				// waiting out a backoff, and re-resolve as part of it.
				conn.ResetConnectBackoff()
			}
		}
	}
}
