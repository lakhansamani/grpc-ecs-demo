// Command productsd serves ProductService: the catalogue.
//
// Read-only and stateless at runtime - its SQLite database, including the FTS5
// search index, is baked into the image. Every task ships an identical file, so
// this is one of the services you scale out when browse traffic spikes.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	productv1 "github.com/lakhansamani/grpc-ecs-ecom/gen/go/product/v1"
	"github.com/lakhansamani/grpc-ecs-ecom/internal/platform/config"
	"github.com/lakhansamani/grpc-ecs-ecom/internal/platform/grpcserver"
	"github.com/lakhansamani/grpc-ecs-ecom/internal/platform/observability"
	"github.com/lakhansamani/grpc-ecs-ecom/internal/platform/store"
	"github.com/lakhansamani/grpc-ecs-ecom/internal/product"
)

const serviceName = "productsd"

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
	dbDriver := cfg.Optional("DB_DRIVER", "sqlite")
	dbURL := cfg.Optional("DB_URL", "file:/data/product.db")
	grpcAddr := cfg.Optional("GRPC_ADDR", ":50053")
	metricsAddr := cfg.Optional("METRICS_ADDR", ":9093")
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

	db, err := store.Open(store.Config{Driver: dbDriver, URL: dbURL}, product.Models()...)
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

	productv1.RegisterProductServiceServer(srv.GRPC(),
		product.NewService(product.NewStore(db), log))

	log.Info("starting", "service", serviceName, "version", version,
		"db_driver", dbDriver, "env", env)
	return srv.Serve(ctx)
}
