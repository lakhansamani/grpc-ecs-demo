package explain

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// Options selects a provider. Driven by env in cmd/paymentd.
type Options struct {
	// Provider: "template" (default) or "bedrock".
	Provider string
	Region   string
	ModelID  string
}

// New returns an Explainer for the requested provider.
//
// "template" is the default and the deployed choice for this demo, because the
// ECS environment has no Bedrock access. "bedrock" is always wrapped in
// Fallback, so even when enabled it cannot break the RPC - it degrades to the
// template instead.
func New(ctx context.Context, o Options, log *slog.Logger) (Explainer, error) {
	switch strings.ToLower(strings.TrimSpace(o.Provider)) {
	case "", "template":
		return Template{}, nil

	case "bedrock":
		b, err := NewBedrock(ctx, o.Region, o.ModelID)
		if err != nil {
			// Misconfiguration should not stop the service from starting; an
			// explanation is a nicety.
			log.Warn("bedrock explainer unavailable, using template", "err", err)
			return Template{}, nil
		}
		log.Info("bedrock explainer enabled",
			"model", o.ModelID,
			"note", "endpoint comes from AWS_ENDPOINT_URL_BEDROCK_RUNTIME if set")
		return Fallback{Primary: b}, nil

	default:
		return nil, fmt.Errorf("explain: unknown provider %q (want template or bedrock)", o.Provider)
	}
}
