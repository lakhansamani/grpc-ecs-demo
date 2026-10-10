// Command orderd serves OrderService.
//
// The only stateful service here, so it runs as ONE task - you scale reads,
// not writes. Every CreateOrder makes two outbound gRPC calls: UserService for
// identity, ProductService for authoritative pricing and stock.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"google.golang.org/grpc"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	orderv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/order/v1"
	productv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/product/v1"
	userv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/user/v1"
	"github.com/lakhansamani/grpc-ecs-demo/internal/order"
	"github.com/lakhansamani/grpc-ecs-demo/internal/platform/config"
	"github.com/lakhansamani/grpc-ecs-demo/internal/platform/grpcclient"
	"github.com/lakhansamani/grpc-ecs-demo/internal/platform/grpcserver"
	"github.com/lakhansamani/grpc-ecs-demo/internal/platform/observability"
	"github.com/lakhansamani/grpc-ecs-demo/internal/platform/store"
)

const serviceName = "orderd"

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
	dbDriver := cfg.Optional("DB_DRIVER", "sqlite")
	dbURL := cfg.Optional("DB_URL", "file:/data/order.db")
	grpcAddr := cfg.Optional("GRPC_ADDR", ":50052")
	metricsAddr := cfg.Optional("METRICS_ADDR", ":9092")
	otlpEndpoint := cfg.Optional("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	env := cfg.Optional("ENVIRONMENT", "local")
	maxConnAge := cfg.Duration("GRPC_MAX_CONNECTION_AGE", 0)
	shutdownTimeout := cfg.Duration("SHUTDOWN_TIMEOUT", 0)
	if err := cfg.Err(); err != nil {
		return err
	}

	ctx := context.Background()

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

	// Each service owns its OWN database on one shared instance. Terraform
	// cannot create them (CREATE DATABASE is SQL, not an AWS API call), so the
	// service does it at boot. No-op on SQLite. See store.EnsureDatabase.
	if err := store.EnsureDatabase(store.Config{Driver: dbDriver, URL: dbURL}); err != nil {
		return err
	}

	db, err := store.Open(store.Config{Driver: dbDriver, URL: dbURL}, order.Models()...)
	if err != nil {
		return err
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// Two clients, both with round_robin over dns:/// - see grpcclient for why
	// both halves of that matter.
	userConn, err := grpcclient.Dial(userAddr, grpcclient.Options{
		TracerProvider: tp, Registry: registry, Subsystem: "user",
	})
	if err != nil {
		return err
	}
	defer func() { _ = userConn.Close() }()

	productConn, err := grpcclient.Dial(productAddr, grpcclient.Options{
		TracerProvider: tp, Registry: registry, Subsystem: "product",
	})
	if err != nil {
		return err
	}
	defer func() { _ = productConn.Close() }()

	// Connect to the upstreams before we start accepting traffic, so the
	// first real request does not pay for the handshake - or fail outright.
	// Not fatal: they may legitimately come up after us.
	for name, conn := range map[string]*grpc.ClientConn{"user": userConn, "product": productConn} {
		if err := grpcclient.Warm(ctx, conn, 5*time.Second); err != nil {
			log.Warn("upstream not ready at boot, will connect on demand", "upstream", name, "err", err)
		} else {
			log.Info("upstream ready", "upstream", name)
		}
	}

	srv := grpcserver.New(grpcserver.Config{
		ServiceName:      serviceName,
		GRPCAddr:         grpcAddr,
		MetricsAddr:      metricsAddr,
		MaxConnectionAge: maxConnAge,
		ShutdownTimeout:  shutdownTimeout,
		TracerProvider:   tp,
		Registry:         registry,
	}, log)

	orderv1.RegisterOrderServiceServer(srv.GRPC(), order.NewService(
		order.NewStore(db),
		userv1.NewUserServiceClient(userConn),
		productv1.NewProductServiceClient(productConn),
		log,
	))

	log.Info("starting", "service", serviceName, "version", version,
		"user_addr", userAddr, "product_addr", productAddr,
		"db_driver", dbDriver, "env", env)
	return srv.Serve(ctx)
}
