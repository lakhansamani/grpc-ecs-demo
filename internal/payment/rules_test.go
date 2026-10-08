package payment

import (
	"testing"
	"time"

	"github.com/lakhansamani/grpc-ecs-payments/internal/payment/explain"
)

func TestEvaluate(t *testing.T) {
	r := DefaultRules()

	for name, tc := range map[string]struct {
		in         Input
		wantReason explain.Reason
		approved   bool
	}{
		"ordinary purchase is approved": {
			in:       Input{AmountMinor: 120000, Currency: "INR", MerchantCategory: "5411"},
			approved: true,
		},
		"exactly at the per-txn limit is approved": {
			in:       Input{AmountMinor: 2_500_000, Currency: "INR", MerchantCategory: "5411"},
			approved: true,
		},
		"one paisa over the limit is declined": {
			in:         Input{AmountMinor: 2_500_001, Currency: "INR", MerchantCategory: "5411"},
			wantReason: explain.ReasonLimitExceeded,
		},
		"blocked merchant category": {
			in:         Input{AmountMinor: 100, Currency: "INR", MerchantCategory: "7995"},
			wantReason: explain.ReasonMerchantBlocked,
		},
		"velocity: at the limit declines": {
			in:         Input{AmountMinor: 100, Currency: "INR", MerchantCategory: "5411", AttemptsInWindow: 3},
			wantReason: explain.ReasonVelocity,
		},
		"velocity: just under the limit is approved": {
			in:       Input{AmountMinor: 100, Currency: "INR", MerchantCategory: "5411", AttemptsInWindow: 2},
			approved: true,
		},
		"insufficient funds once the balance is spent": {
			in:         Input{AmountMinor: 200000, Currency: "INR", MerchantCategory: "5411", SpentMinor: 4_900_000},
			wantReason: explain.ReasonInsufficientFunds,
		},
	} {
		got := r.Evaluate(tc.in)
		if got.Approved() != tc.approved {
			t.Errorf("%s: approved = %v, want %v (reason %v)", name, got.Approved(), tc.approved, got.Reason)
			continue
		}
		if !tc.approved && got.Reason != tc.wantReason {
			t.Errorf("%s: reason = %v, want %v", name, got.Reason, tc.wantReason)
		}
	}
}

// A blocked merchant wins over an oversized amount: the cardholder gets the
// reason they can act on.
func TestEvaluateReasonPrecedence(t *testing.T) {
	out := DefaultRules().Evaluate(Input{
		AmountMinor: 99_999_999, Currency: "INR", MerchantCategory: "7995", AttemptsInWindow: 99,
	})
	if out.Reason != explain.ReasonMerchantBlocked {
		t.Fatalf("reason = %v, want MerchantBlocked", out.Reason)
	}
}

// Facts must be populated well enough for an Explainer to render prose, and
// AttemptsInWindow must count the current attempt.
func TestEvaluateFactsAreExplainable(t *testing.T) {
	out := DefaultRules().Evaluate(Input{
		AmountMinor: 4_500_000, Currency: "INR", MerchantCategory: "5411", AttemptsInWindow: 1,
	})
	if out.Facts.Decision != explain.Declined || out.Facts.Reason != explain.ReasonLimitExceeded {
		t.Fatalf("facts do not mirror the outcome: %+v", out.Facts)
	}
	if out.Facts.AttemptsInWindow != 2 {
		t.Errorf("AttemptsInWindow = %d, want 2 (prior attempts + this one)", out.Facts.AttemptsInWindow)
	}
	if out.Facts.WindowMinutes != 10 {
		t.Errorf("WindowMinutes = %d, want 10", out.Facts.WindowMinutes)
	}
	text, _, err := explain.Template{}.Explain(t.Context(), out.Facts)
	if err != nil || text == "" {
		t.Fatalf("facts did not render: %q %v", text, err)
	}
}

// Balance must never go negative and must not wrap into a huge allowance.
func TestEvaluateOverspentBalanceClamps(t *testing.T) {
	out := DefaultRules().Evaluate(Input{
		AmountMinor: 100, Currency: "INR", MerchantCategory: "5411",
		SpentMinor: 99_999_999,
	})
	if out.Facts.BalanceMinor != 0 {
		t.Fatalf("BalanceMinor = %d, want 0", out.Facts.BalanceMinor)
	}
	if out.Approved() {
		t.Fatal("approved against a zero balance")
	}
}

func TestVelocityWindowIsTenMinutes(t *testing.T) {
	if DefaultRules().VelocityWindow != 10*time.Minute {
		t.Fatal("velocity window changed; update the explanation copy too")
	}
}
