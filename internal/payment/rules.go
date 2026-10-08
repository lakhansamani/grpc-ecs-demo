// Package payment implements PaymentService: issuer-side authorization.
package payment

import (
	"strings"
	"time"

	"github.com/lakhansamani/grpc-ecs-payments/internal/payment/explain"
)

// Rules is the authorization policy. Deterministic and table-driven on
// purpose: in a regulated domain the decision must be reproducible and
// explainable from its inputs, which is also why the language model in
// internal/payment/explain can only phrase a decision, never make one.
type Rules struct {
	PerTxnLimitMinor    int64
	VelocityLimit       int
	VelocityWindow      time.Duration
	BlockedCategories   map[string]struct{}
	OpeningBalanceMinor int64
}

// DefaultRules are the demo's limits: ₹25,000 per transaction, 3 authorizations
// per 10 minutes, gambling (MCC 7995) and crypto (6051) blocked, and a
// ₹50,000 opening balance per user.
func DefaultRules() Rules {
	return Rules{
		PerTxnLimitMinor: 2_500_000,
		VelocityLimit:    3,
		VelocityWindow:   10 * time.Minute,
		BlockedCategories: map[string]struct{}{
			"7995": {}, // gambling
			"6051": {}, // quasi-cash / crypto
		},
		OpeningBalanceMinor: 5_000_000,
	}
}

// Input is everything the policy needs. Gathered by the service; the rules
// themselves touch no database and no clock, so they are trivially testable.
type Input struct {
	AmountMinor      int64
	Currency         string
	MerchantID       string
	MerchantCategory string
	// AttemptsInWindow counts authorizations already made in VelocityWindow,
	// excluding the one being evaluated.
	AttemptsInWindow int
	// SpentMinor is the sum of previously APPROVED authorizations.
	SpentMinor int64
}

// Outcome is the decision plus the facts that justify it, ready to hand to an
// Explainer.
type Outcome struct {
	Decision explain.Decision
	Reason   explain.Reason
	Facts    explain.Facts
}

// Approved reports whether the authorization succeeded.
func (o Outcome) Approved() bool { return o.Decision == explain.Approved }

// Evaluate applies the policy.
//
// Order is fixed and significant: a blocked merchant is reported as such even
// if the amount would also have failed, so the cardholder gets the most
// actionable reason rather than whichever check happened to run first.
func (r Rules) Evaluate(in Input) Outcome {
	balance := r.OpeningBalanceMinor - in.SpentMinor
	if balance < 0 {
		balance = 0
	}

	facts := explain.Facts{
		AmountMinor:      in.AmountMinor,
		Currency:         in.Currency,
		PerTxnLimitMinor: r.PerTxnLimitMinor,
		BalanceMinor:     balance,
		AttemptsInWindow: in.AttemptsInWindow + 1, // including this one
		VelocityLimit:    r.VelocityLimit,
		WindowMinutes:    int(r.VelocityWindow / time.Minute),
		MerchantID:       in.MerchantID,
		MerchantCategory: in.MerchantCategory,
	}

	decline := func(reason explain.Reason) Outcome {
		facts.Decision = explain.Declined
		facts.Reason = reason
		return Outcome{Decision: explain.Declined, Reason: reason, Facts: facts}
	}

	if _, blocked := r.BlockedCategories[strings.TrimSpace(in.MerchantCategory)]; blocked {
		return decline(explain.ReasonMerchantBlocked)
	}
	if r.PerTxnLimitMinor > 0 && in.AmountMinor > r.PerTxnLimitMinor {
		return decline(explain.ReasonLimitExceeded)
	}
	if r.VelocityLimit > 0 && in.AttemptsInWindow >= r.VelocityLimit {
		return decline(explain.ReasonVelocity)
	}
	if in.AmountMinor > balance {
		return decline(explain.ReasonInsufficientFunds)
	}

	facts.Decision = explain.Approved
	facts.Reason = explain.ReasonNone
	return Outcome{Decision: explain.Approved, Reason: explain.ReasonNone, Facts: facts}
}
