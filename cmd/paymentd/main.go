// Command paymentd serves PaymentService.
//
// Holds writable state (its SQLite file), so it runs as ONE task. That is not a
// shortcut being hidden - it is the demo's closing lesson: kill the task and the
// authorizations are gone, which is what "don't keep state in the task" means.
// Scale identityd, not this.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	identityv1 "github.com/lakhansamani/grpc-ecs-payments/gen/go/identity/v1"
	paymentv1 "github.com/lakhansamani/grpc-ecs-payments/gen/go/payment/v1"
	"github.com/lakhansamani/grpc-ecs-payments/internal/payment"
	"github.com/lakhansamani/grpc-ecs-payments/internal/payment/explain"
	"github.com/lakhansamani/grpc-ecs-payments/internal/platform/config"
	"github.com/lakhansamani/grpc-ecs-payments/internal/platform/grpcclient"
	"github.com/lakhansamani/grpc-ecs-payments/internal/platform/grpcserver"
	"github.com/lakhansamani/grpc-ecs-payments/internal/platform/observability"
	"github.com/lakhansamani/grpc-ecs-payments/internal/platform/store"
)

const serviceName = "paymentd"

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
	identityAddr := cfg.Required("IDENTITY_ADDR")
	dbDriver := cfg.Optional("DB_DRIVER", "sqlite")
	dbURL := cfg.Optional("DB_URL", "file:/data/payment.db")
	grpcAddr := cfg.Optional("GRPC_ADDR", ":50052")
	metricsAddr := cfg.Optional("METRICS_ADDR", ":9092")
	otlpEndpoint := cfg.Optional("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	env := cfg.Optional("ENVIRONMENT", "local")
	// No Bedrock access on the ECS deployment, so "template" is the default.
	// See SPEC.md 6.8 - the bedrock path is opt-in and always wrapped in a
	// fallback, and its endpoint comes from AWS_ENDPOINT_URL_BEDROCK_RUNTIME.
	llmProvider := cfg.Optional("LLM_PROVIDER", "template")
	llmModel := cfg.Optional("LLM_MODEL_ID", "amazon.nova-micro-v1:0")
	awsRegion := cfg.Optional("AWS_REGION", "us-east-1")
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

	db, err := store.Open(store.Config{Driver: dbDriver, URL: dbURL}, payment.Models()...)
	if err != nil {
		return err
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// The client that makes the load-balancing lesson work. See grpcclient.
	conn, err := grpcclient.Dial(identityAddr, grpcclient.Options{
		TracerProvider: tp,
		Registry:       registry,
	})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	explainer, err := explain.New(ctx, explain.Options{
		Provider: llmProvider,
		Region:   awsRegion,
		ModelID:  llmModel,
	}, log)
	if err != nil {
		return err
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

	paymentv1.RegisterPaymentServiceServer(srv.GRPC(), payment.NewService(
		payment.NewStore(db),
		identityv1.NewIdentityServiceClient(conn),
		payment.DefaultRules(),
		explainer,
		log,
	))

	log.Info("starting", "service", serviceName, "version", version,
		"identity_addr", identityAddr, "db_driver", dbDriver,
		"llm_provider", llmProvider, "env", env)
	return srv.Serve(ctx)
}
