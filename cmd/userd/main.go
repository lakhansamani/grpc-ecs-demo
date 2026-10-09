// Command identityd serves UserService.
//
// Stateless by design: its SQLite database is baked into the image, so every
// task answers reads identically and the service can be scaled horizontally.
// This is the service to scale to 3 tasks for the load-balancing demo.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	userv1 "github.com/lakhansamani/grpc-ecs-payments/gen/go/user/v1"
	"github.com/lakhansamani/grpc-ecs-payments/internal/platform/config"
	"github.com/lakhansamani/grpc-ecs-payments/internal/platform/grpcserver"
	"github.com/lakhansamani/grpc-ecs-payments/internal/platform/observability"
	"github.com/lakhansamani/grpc-ecs-payments/internal/platform/store"
	"github.com/lakhansamani/grpc-ecs-payments/internal/user"
)

const serviceName = "userd"

// version is stamped at build time: -ldflags "-X main.version=..."
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
	// Shared across every task - see SPEC.md 6.6. Sourced from Secrets Manager.
	jwtSecret := cfg.Required("JWT_SECRET")
	dbDriver := cfg.Optional("DB_DRIVER", "sqlite")
	dbURL := cfg.Optional("DB_URL", "file:/data/user.db")
	grpcAddr := cfg.Optional("GRPC_ADDR", ":50051")
	metricsAddr := cfg.Optional("METRICS_ADDR", ":9091")
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
		// Checked, unlike the version this replaces (defect #10), which also
		// never reached its deferred shutdown because Serve blocked until
		// log.Fatalf - losing every buffered span on exit.
		if err := shutdownTracing(context.Background()); err != nil {
			log.Warn("tracer shutdown", "err", err)
		}
	}()

	db, err := store.Open(store.Config{Driver: dbDriver, URL: dbURL}, user.Models()...)
	if err != nil {
		return err
	}

	issuer, err := user.NewIssuer(jwtSecret)
	if err != nil {
		return err
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	srv := grpcserver.New(grpcserver.Config{
		ServiceName:      serviceName,
		GRPCAddr:         grpcAddr,
		MetricsAddr:      metricsAddr,
		MaxConnectionAge: maxConnAge,
		ShutdownTimeout:  shutdownTimeout,
		TracerProvider:   tp,
		Registry:         registry,
	}, log)

	userv1.RegisterUserServiceServer(
		srv.GRPC(),
		user.NewService(user.NewStore(db), issuer, log),
	)

	log.Info("starting", "service", serviceName, "version", version,
		"db_driver", dbDriver, "env", env)
	return srv.Serve(ctx)
}
