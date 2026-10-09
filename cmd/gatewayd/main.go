// Command gatewayd is the REST front door.
//
// It speaks JSON over HTTP/1.1 to the outside world and gRPC to everything
// inside. The routing table is not written here - it is GENERATED from the
// same .proto files that produce the gRPC stubs, from the google.api.http
// annotations on each RPC.
//
// Two consequences worth knowing:
//
//   - An RPC without an http annotation gets no route. ProductService's
//     CheckAvailability is internal-only for exactly that reason: a client must
//     not be able to look up the authoritative price and then submit a
//     different one. The absence of four lines of proto enforces it, not a
//     check somebody has to remember to write.
//
//   - This is the only process a browser can talk to. gRPC needs HTTP/2
//     trailers, which browsers cannot send - which is also why none of this
//     runs on Lambda.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"

	orderv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/order/v1"
	productv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/product/v1"
	userv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/user/v1"
	"github.com/lakhansamani/grpc-ecs-demo/internal/platform/config"
	"github.com/lakhansamani/grpc-ecs-demo/internal/platform/grpcclient"
	"github.com/lakhansamani/grpc-ecs-demo/internal/platform/observability"
)

const serviceName = "gatewayd"

var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := config.New()
	userAddr := cfg.Required("USER_ADDR")
	productAddr := cfg.Required("PRODUCT_ADDR")
	orderAddr := cfg.Required("ORDER_ADDR")
	httpAddr := cfg.Optional("HTTP_ADDR", ":8080")
	metricsAddr := cfg.Optional("METRICS_ADDR", ":9094")
	otlpEndpoint := cfg.Optional("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	env := cfg.Optional("ENVIRONMENT", "local")
	shutdownTimeout := cfg.Duration("SHUTDOWN_TIMEOUT", 15*time.Second)
	if err := cfg.Err(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	tp, shutdownTracing, err := observability.Init(ctx, observability.Config{
		ServiceName:  serviceName,
		Version:      version,
		Environment:  env,
		OTLPEndpoint: otlpEndpoint,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := shutdownTracing(context.Background()); err != nil {
			log.Warn("tracer shutdown", "err", err)
		}
	}()

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// Three outbound clients, each round-robin over dns:/// and each with its
	// own metric prefix. The gateway is a third consumer of userd, alongside
	// orderd - which is why userd is the busiest service in the system.
	conns := map[string]*grpc.ClientConn{}
	for name, addr := range map[string]string{
		"user": userAddr, "product": productAddr, "order": orderAddr,
	} {
		conn, err := grpcclient.Dial(addr, grpcclient.Options{
			TracerProvider: tp, Registry: registry, Subsystem: name,
		})
		if err != nil {
			return err
		}
		defer func(c *grpc.ClientConn) { _ = c.Close() }(conn)
		conns[name] = conn
	}

	for name, conn := range conns {
		if err := grpcclient.Warm(ctx, conn, 5*time.Second); err != nil {
			log.Warn("upstream not ready at boot, will connect on demand", "upstream", name, "err", err)
		}
	}

	// The default header matcher already forwards Authorization into gRPC
	// metadata as "authorization", which is what the services read.
	mux := runtime.NewServeMux(
		runtime.WithErrorHandler(runtime.DefaultHTTPErrorHandler),
	)
	if err := userv1.RegisterUserServiceHandler(ctx, mux, conns["user"]); err != nil {
		return err
	}
	if err := productv1.RegisterProductServiceHandler(ctx, mux, conns["product"]); err != nil {
		return err
	}
	if err := orderv1.RegisterOrderServiceHandler(ctx, mux, conns["order"]); err != nil {
		return err
	}

	root := http.NewServeMux()
	root.Handle("/v1/", mux)
	// Plain HTTP health, for the container healthCheck and an ALB target group.
	root.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"SERVING"}`))
	})

	srv := &http.Server{
		Addr:              httpAddr,
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
	}
	metricsSrv := &http.Server{
		Addr:              metricsAddr,
		Handler:           metricsHandler(registry),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server stopped", "err", err)
		}
	}()

	serveErr := make(chan error, 1)
	go func() {
		log.Info("starting", "service", serviceName, "version", version,
			"http_addr", httpAddr, "user_addr", userAddr,
			"product_addr", productAddr, "order_addr", orderAddr, "env", env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received, draining")
	}

	// Same discipline as the gRPC services: drain in-flight requests under a
	// deadline that sits below the ECS task stopTimeout.
	drainCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		log.Warn("drain did not finish cleanly", "err", err)
	} else {
		log.Info("drained cleanly")
	}
	_ = metricsSrv.Shutdown(drainCtx)
	return nil
}

func metricsHandler(reg *prometheus.Registry) http.Handler {
	m := http.NewServeMux()
	m.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	return m
}
