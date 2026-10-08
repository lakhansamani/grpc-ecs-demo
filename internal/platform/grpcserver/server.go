// Package grpcserver builds a gRPC server configured the way a service running
// on ECS actually needs to be configured.
//
// Three things here are the point of the talk and are missing from most
// examples (including the repo this demo replaces):
//
//  1. grpc.health.v1 is registered, so the container healthCheck and the ALB
//     target group have something real to ask.
//  2. SIGTERM is handled: the health status flips to NOT_SERVING first so load
//     balancers stop sending new work, then in-flight RPCs are drained with
//     GracefulStop under a deadline. ECS sends SIGTERM and then SIGKILLs after
//     stopTimeout, so draining has to be bounded.
//  3. MaxConnectionAge is set. gRPC clients hold one long-lived HTTP/2
//     connection and multiplex every call over it, so without this a client
//     that connected before a scale-out NEVER discovers the new tasks. This is
//     the single most common gRPC-on-ECS bug.
package grpcserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
)

// Config describes how to run the server.
type Config struct {
	ServiceName string
	GRPCAddr    string // e.g. ":50051"
	MetricsAddr string // e.g. ":9091"

	// MaxConnectionAge forces clients to reconnect (and therefore re-resolve
	// DNS) periodically. Without it, horizontal scaling is invisible to
	// existing clients. See the package comment.
	MaxConnectionAge      time.Duration
	MaxConnectionAgeGrace time.Duration

	// ShutdownTimeout bounds the drain. Keep it below the ECS task
	// stopTimeout, or SIGKILL will cut the drain short anyway.
	ShutdownTimeout time.Duration

	TracerProvider trace.TracerProvider
	Registry       *prometheus.Registry
}

func (c *Config) setDefaults() {
	if c.MaxConnectionAge == 0 {
		c.MaxConnectionAge = 30 * time.Second
	}
	if c.MaxConnectionAgeGrace == 0 {
		c.MaxConnectionAgeGrace = 5 * time.Second
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = 15 * time.Second
	}
	if c.GRPCAddr == "" {
		c.GRPCAddr = ":50051"
	}
	if c.MetricsAddr == "" {
		c.MetricsAddr = ":9091"
	}
}

// Server wraps a configured *grpc.Server plus its health and metrics plumbing.
type Server struct {
	cfg    Config
	grpc   *grpc.Server
	health *health.Server
	log    *slog.Logger
}

// New builds the server. Register your service implementations on Server.GRPC()
// before calling Serve.
func New(cfg Config, log *slog.Logger) *Server {
	cfg.setDefaults()

	srvMetrics := grpcprom.NewServerMetrics()
	cfg.Registry.MustRegister(srvMetrics)

	opts := []grpc.ServerOption{
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionAge:      cfg.MaxConnectionAge,
			MaxConnectionAgeGrace: cfg.MaxConnectionAgeGrace,
		}),
		// Reject clients that ping too aggressively rather than being DoS'd by them.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.ChainUnaryInterceptor(srvMetrics.UnaryServerInterceptor()),
	}
	if cfg.TracerProvider != nil {
		opts = append(opts, grpc.StatsHandler(
			otelgrpc.NewServerHandler(otelgrpc.WithTracerProvider(cfg.TracerProvider)),
		))
	}

	s := grpc.NewServer(opts...)

	hs := health.NewServer()
	healthpb.RegisterHealthServer(s, hs)
	// Reflection makes grpcurl work without a local copy of the protos, which
	// is worth a lot when debugging a task you cannot attach to.
	reflection.Register(s)

	return &Server{cfg: cfg, grpc: s, health: hs, log: log}
}

// GRPC exposes the underlying server for service registration.
func (s *Server) GRPC() *grpc.Server { return s.grpc }

// Serve listens, serves, and blocks until SIGTERM/SIGINT, then drains.
// It returns nil on a clean shutdown.
func (s *Server) Serve(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	lis, err := net.Listen("tcp", s.cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.GRPCAddr, err)
	}

	metricsSrv := &http.Server{
		Addr:              s.cfg.MetricsAddr,
		Handler:           s.metricsHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// A dead metrics endpoint must not take the service down.
			s.log.Error("metrics server stopped", "err", err)
		}
	}()

	// Only now do we report SERVING: everything is actually wired up.
	s.health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	s.health.SetServingStatus(s.cfg.ServiceName, healthpb.HealthCheckResponse_SERVING)

	serveErr := make(chan error, 1)
	go func() {
		s.log.Info("grpc serving", "addr", s.cfg.GRPCAddr, "service", s.cfg.ServiceName)
		if err := s.grpc.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		s.log.Info("shutdown signal received, draining")
	}

	// 1. Fail health checks first. Load balancers and ECS stop sending new
	//    work before we stop accepting it, which is what makes a deploy
	//    invisible to callers.
	s.health.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	s.health.SetServingStatus(s.cfg.ServiceName, healthpb.HealthCheckResponse_NOT_SERVING)
	s.health.Shutdown()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()

	// 2. Drain in-flight RPCs, bounded.
	drained := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(drained)
	}()

	select {
	case <-drained:
		s.log.Info("drained cleanly")
	case <-shutdownCtx.Done():
		// 3. Out of time: ECS is about to SIGKILL us anyway. Being explicit
		//    beats being killed mid-write.
		s.log.Warn("drain timed out, forcing stop", "timeout", s.cfg.ShutdownTimeout)
		s.grpc.Stop()
	}

	_ = metricsSrv.Shutdown(shutdownCtx)
	return nil
}

func (s *Server) metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(s.cfg.Registry, promhttp.HandlerOpts{}))
	return mux
}
