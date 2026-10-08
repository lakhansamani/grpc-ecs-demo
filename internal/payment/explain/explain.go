// Package explain turns an authorization decision into a sentence a customer
// could read.
//
// The rules decide; this only narrates. Nothing in here can approve or decline
// anything - that separation is deliberate and is the point worth making on
// stage: in a regulated domain you do not let a language model make the call,
// you let it phrase a call that deterministic, auditable code already made.
//
// Providers:
//
//	Template - pure Go, no network, no credentials, identical output every time.
//	           The DEFAULT, including on ECS where there is no Bedrock access.
//	Bedrock  - the real AWS SDK bedrockruntime client. Opt-in. Its endpoint is
//	           overridable through the SDK's own AWS_ENDPOINT_URL_BEDROCK_RUNTIME
//	           variable, so the same binary talks to Ministack locally, to a
//	           stub endpoint on ECS, or to real Bedrock where access exists -
//	           with no code change and no "if local" branch.
//
// Fallback wraps any provider so a failure degrades to Template rather than
// failing the RPC. An explanation is a nicety; never fail a payment lookup for it.
package explain

import (
	"context"
	"fmt"
	"strings"
)

// Decision mirrors the proto enum without importing it, so this package stays
// independent of the wire format.
type Decision int

const (
	Approved Decision = iota
	Declined
)

// Reason mirrors payment.v1.DeclineReason.
type Reason int

const (
	ReasonNone Reason = iota
	ReasonLimitExceeded
	ReasonVelocity
	ReasonMerchantBlocked
	ReasonInsufficientFunds
)

// Facts is everything the rules knew when they decided. Explainers may only
// read it.
type Facts struct {
	Decision         Decision
	Reason           Reason
	AmountMinor      int64
	Currency         string
	PerTxnLimitMinor int64
	BalanceMinor     int64
	AttemptsInWindow int
	VelocityLimit    int
	WindowMinutes    int
	MerchantID       string
	MerchantCategory string
}

// Explainer renders Facts as prose and names the provider that did it.
type Explainer interface {
	Explain(ctx context.Context, f Facts) (text string, provider string, err error)
}

// Template is a deterministic, dependency-free Explainer.
//
// For a decline reason this is arguably the right production choice as well as
// the safe demo choice: it is auditable, instant, free, translatable, and it
// cannot hallucinate a reason the rules did not give.
type Template struct{}

func (Template) Explain(_ context.Context, f Facts) (string, string, error) {
	amount := Money(f.AmountMinor, f.Currency)

	if f.Decision == Approved {
		return fmt.Sprintf("Approved: %s at merchant %s.", amount, merchantOrUnknown(f.MerchantID)),
			"template", nil
	}

	switch f.Reason {
	case ReasonLimitExceeded:
		return fmt.Sprintf(
			"Declined: %s exceeds your per-transaction limit of %s.",
			amount, Money(f.PerTxnLimitMinor, f.Currency)), "template", nil

	case ReasonVelocity:
		return fmt.Sprintf(
			"Declined: this is authorization attempt %d in the last %d minutes, and the limit is %d. Try again shortly.",
			f.AttemptsInWindow, f.WindowMinutes, f.VelocityLimit), "template", nil

	case ReasonMerchantBlocked:
		return fmt.Sprintf(
			"Declined: merchant category %s is blocked on this account.",
			categoryOrUnknown(f.MerchantCategory)), "template", nil

	case ReasonInsufficientFunds:
		return fmt.Sprintf(
			"Declined: available balance %s is less than %s.",
			Money(f.BalanceMinor, f.Currency), amount), "template", nil

	default:
		// Never leave a decline unexplained, even for a reason added later.
		return fmt.Sprintf("Declined: %s could not be authorized.", amount), "template", nil
	}
}

// Fallback tries Primary and silently degrades to Template on any error or
// empty result. Deployed everywhere, because a stage demo must not break when
// an optional dependency is unreachable.
type Fallback struct {
	Primary Explainer
}

func (fb Fallback) Explain(ctx context.Context, f Facts) (string, string, error) {
	if fb.Primary != nil {
		text, provider, err := fb.Primary.Explain(ctx, f)
		if err == nil && strings.TrimSpace(text) != "" {
			return text, provider, nil
		}
	}
	text, provider, _ := Template{}.Explain(ctx, f)
	return text, provider + " (fallback)", nil
}

// Money renders minor units as major units with two decimals.
// ponytail: no Indian lakh/crore digit grouping - add it if the projector
// makes long numbers hard to read.
func Money(minor int64, currency string) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	symbol := symbolFor(currency)
	out := fmt.Sprintf("%s%d.%02d", symbol, minor/100, minor%100)
	if neg {
		return "-" + out
	}
	return out
}

func symbolFor(currency string) string {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "INR", "":
		return "₹"
	case "USD":
		return "$"
	case "EUR":
		return "€"
	default:
		return strings.ToUpper(currency) + " "
	}
}

func merchantOrUnknown(id string) string {
	if strings.TrimSpace(id) == "" {
		return "unknown"
	}
	return id
}

func categoryOrUnknown(c string) string {
	if strings.TrimSpace(c) == "" {
		return "unknown"
	}
	return c
}
