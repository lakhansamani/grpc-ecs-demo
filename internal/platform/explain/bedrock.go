package explain

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// Bedrock asks a model to phrase the decision.
//
// There is no endpoint handling here on purpose. The AWS SDK already reads
// AWS_ENDPOINT_URL_BEDROCK_RUNTIME (its generic AWS_ENDPOINT_URL_<SDK_ID>
// mechanism), so the deployment decides where this points:
//
//	local   -> http://ministack:4566   (emulated Converse)
//	ECS     -> http://mock-bedrock:8080 or unset with provider=template
//	real    -> unset, hits Bedrock via the task role
//
// Exactly the same trick as the Terraform `endpoints` block, which is why it is
// worth pointing at on stage: one env var, no "if local" branch anywhere.
type Bedrock struct {
	client  *bedrockruntime.Client
	modelID string
}

// NewBedrock builds the client from the ambient AWS config (task role on ECS,
// env/profile locally). Returns an error only if config loading fails - it
// deliberately does not probe the endpoint, because Fallback handles that.
func NewBedrock(ctx context.Context, region, modelID string) (*Bedrock, error) {
	opts := []func(*config.LoadOptions) error{}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("explain: load aws config: %w", err)
	}
	if modelID == "" {
		modelID = "amazon.nova-micro-v1:0"
	}
	return &Bedrock{client: bedrockruntime.NewFromConfig(cfg), modelID: modelID}, nil
}

const systemPrompt = `You rewrite a card authorization decision as one short sentence for the cardholder.
Rules you must follow:
- Use ONLY the facts given. Never invent a reason, amount or limit.
- Never contradict the stated decision.
- One sentence, under 40 words, plain English, no greeting, no apology.`

func (b *Bedrock) Explain(ctx context.Context, f Facts) (string, string, error) {
	out, err := b.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String(b.modelID),
		System: []brtypes.SystemContentBlock{
			&brtypes.SystemContentBlockMemberText{Value: systemPrompt},
		},
		Messages: []brtypes.Message{{
			Role: brtypes.ConversationRoleUser,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberText{Value: factSheet(f)},
			},
		}},
		InferenceConfig: &brtypes.InferenceConfiguration{
			MaxTokens:   aws.Int32(120),
			Temperature: aws.Float32(0.2),
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("explain: converse: %w", err)
	}

	msg, ok := out.Output.(*brtypes.ConverseOutputMemberMessage)
	if !ok {
		return "", "", fmt.Errorf("explain: unexpected converse output %T", out.Output)
	}
	var sb strings.Builder
	for _, block := range msg.Value.Content {
		if t, ok := block.(*brtypes.ContentBlockMemberText); ok {
			sb.WriteString(t.Value)
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return "", "", fmt.Errorf("explain: empty completion")
	}
	return text, "bedrock:" + b.modelID, nil
}

// factSheet gives the model the facts and nothing else. Keeping this as plain
// labelled lines (rather than prose) makes it obvious in a trace what the model
// was told, which matters when someone disputes the explanation.
func factSheet(f Facts) string {
	var b strings.Builder
	if f.Decision == Approved {
		b.WriteString("decision: APPROVED\n")
	} else {
		b.WriteString("decision: DECLINED\n")
		b.WriteString("reason: " + reasonName(f.Reason) + "\n")
	}
	fmt.Fprintf(&b, "amount: %s\n", Money(f.AmountMinor, f.Currency))
	if f.PerTxnLimitMinor > 0 {
		fmt.Fprintf(&b, "per_transaction_limit: %s\n", Money(f.PerTxnLimitMinor, f.Currency))
	}
	if f.Reason == ReasonInsufficientFunds {
		fmt.Fprintf(&b, "available_balance: %s\n", Money(f.BalanceMinor, f.Currency))
	}
	if f.Reason == ReasonVelocity {
		fmt.Fprintf(&b, "attempts_in_window: %d\nvelocity_limit: %d\nwindow_minutes: %d\n",
			f.AttemptsInWindow, f.VelocityLimit, f.WindowMinutes)
	}
	if f.MerchantCategory != "" {
		fmt.Fprintf(&b, "merchant_category: %s\n", f.MerchantCategory)
	}
	if f.MerchantID != "" {
		fmt.Fprintf(&b, "merchant_id: %s\n", f.MerchantID)
	}
	return b.String()
}

func reasonName(r Reason) string {
	switch r {
	case ReasonLimitExceeded:
		return "PER_TRANSACTION_LIMIT_EXCEEDED"
	case ReasonVelocity:
		return "TOO_MANY_ATTEMPTS"
	case ReasonMerchantBlocked:
		return "MERCHANT_CATEGORY_BLOCKED"
	case ReasonInsufficientFunds:
		return "INSUFFICIENT_FUNDS"
	default:
		return "UNSPECIFIED"
	}
}
