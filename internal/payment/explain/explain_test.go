package explain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMoney(t *testing.T) {
	for _, tc := range []struct {
		minor    int64
		currency string
		want     string
	}{
		{4500000, "INR", "₹45000.00"},
		{2550, "INR", "₹25.50"},
		{0, "INR", "₹0.00"},
		{5, "INR", "₹0.05"},
		{-1999, "INR", "-₹19.99"},
		{150000, "USD", "$1500.00"},
		{1000, "JPY", "JPY 10.00"},
		{999, "", "₹9.99"},
	} {
		if got := Money(tc.minor, tc.currency); got != tc.want {
			t.Errorf("Money(%d,%q) = %q, want %q", tc.minor, tc.currency, got, tc.want)
		}
	}
}

func TestTemplateCoversEveryReason(t *testing.T) {
	base := Facts{Decision: Declined, AmountMinor: 4500000, Currency: "INR"}

	cases := map[Reason]struct {
		mutate   func(*Facts)
		contains []string
	}{
		ReasonLimitExceeded: {
			func(f *Facts) { f.PerTxnLimitMinor = 2500000 },
			[]string{"₹45000.00", "₹25000.00", "limit"},
		},
		ReasonVelocity: {
			func(f *Facts) { f.AttemptsInWindow, f.VelocityLimit, f.WindowMinutes = 4, 3, 10 },
			[]string{"4", "10 minutes", "limit is 3"},
		},
		ReasonMerchantBlocked: {
			func(f *Facts) { f.MerchantCategory = "7995" },
			[]string{"7995", "blocked"},
		},
		ReasonInsufficientFunds: {
			func(f *Facts) { f.BalanceMinor = 120000 },
			[]string{"₹1200.00", "₹45000.00"},
		},
	}

	for reason, tc := range cases {
		f := base
		f.Reason = reason
		tc.mutate(&f)

		text, provider, err := Template{}.Explain(context.Background(), f)
		if err != nil {
			t.Fatalf("reason %v: %v", reason, err)
		}
		if provider != "template" {
			t.Errorf("reason %v: provider = %q", reason, provider)
		}
		if !strings.HasPrefix(text, "Declined:") {
			t.Errorf("reason %v: a decline must say so: %q", reason, text)
		}
		for _, want := range tc.contains {
			if !strings.Contains(text, want) {
				t.Errorf("reason %v: %q missing %q", reason, text, want)
			}
		}
	}
}

// A reason the rules learn about later must still produce something sane.
func TestTemplateUnknownReasonStillExplains(t *testing.T) {
	text, _, err := Template{}.Explain(context.Background(), Facts{
		Decision: Declined, Reason: Reason(99), AmountMinor: 100, Currency: "INR",
	})
	if err != nil || !strings.HasPrefix(text, "Declined:") || text == "" {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func TestTemplateApproved(t *testing.T) {
	text, _, err := Template{}.Explain(context.Background(), Facts{
		Decision: Approved, AmountMinor: 120000, Currency: "INR", MerchantID: "M-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "Approved:") || !strings.Contains(text, "M-123") {
		t.Fatalf("got %q", text)
	}
}

type failing struct{}

func (failing) Explain(context.Context, Facts) (string, string, error) {
	return "", "", errors.New("bedrock is not reachable from this task")
}

type blank struct{}

func (blank) Explain(context.Context, Facts) (string, string, error) { return "   ", "x", nil }

// The deployed guarantee: an unreachable or useless provider never fails the RPC.
func TestFallbackDegradesToTemplate(t *testing.T) {
	for name, primary := range map[string]Explainer{"error": failing{}, "empty": blank{}} {
		text, provider, err := Fallback{Primary: primary}.Explain(context.Background(), Facts{
			Decision: Declined, Reason: ReasonLimitExceeded,
			AmountMinor: 4500000, PerTxnLimitMinor: 2500000, Currency: "INR",
		})
		if err != nil {
			t.Fatalf("%s: Fallback returned an error: %v", name, err)
		}
		if !strings.Contains(text, "₹25000.00") {
			t.Errorf("%s: want the template text, got %q", name, text)
		}
		if !strings.Contains(provider, "fallback") {
			t.Errorf("%s: provider should admit it fell back, got %q", name, provider)
		}
	}
}

// Proves the mocking story: the AWS SDK honours
// AWS_ENDPOINT_URL_BEDROCK_RUNTIME, so a stub endpoint stands in for Bedrock
// with no change to the client code. This is how paymentd gets explanations on
// ECS without Bedrock access.
func TestBedrockHonoursEndpointOverride(t *testing.T) {
	var gotPath string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": map[string]any{"message": map[string]any{
				"role":    "assistant",
				"content": []any{map[string]any{"text": "Declined because the amount is over your limit."}},
			}},
			"stopReason": "end_turn",
			"usage":      map[string]any{"inputTokens": 10, "outputTokens": 11, "totalTokens": 21},
		})
	}))
	defer stub.Close()

	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", stub.URL)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")

	b, err := NewBedrock(context.Background(), "us-east-1", "amazon.nova-micro-v1:0")
	if err != nil {
		t.Fatal(err)
	}
	text, provider, err := b.Explain(context.Background(), Facts{
		Decision: Declined, Reason: ReasonLimitExceeded,
		AmountMinor: 4500000, PerTxnLimitMinor: 2500000, Currency: "INR",
	})
	if err != nil {
		t.Fatalf("Explain against stub endpoint: %v", err)
	}
	if !strings.Contains(text, "over your limit") {
		t.Errorf("text = %q", text)
	}
	if provider != "bedrock:amazon.nova-micro-v1:0" {
		t.Errorf("provider = %q", provider)
	}
	if !strings.Contains(gotPath, "converse") {
		t.Errorf("stub saw path %q, want the converse route", gotPath)
	}
}

// The fact sheet must never leak a limit the rules did not supply.
func TestFactSheetOmitsUnsetFields(t *testing.T) {
	sheet := factSheet(Facts{Decision: Declined, Reason: ReasonLimitExceeded,
		AmountMinor: 100, Currency: "INR"})
	if strings.Contains(sheet, "per_transaction_limit") {
		t.Errorf("unset limit leaked into the prompt:\n%s", sheet)
	}
	if !strings.Contains(sheet, "decision: DECLINED") {
		t.Errorf("sheet missing decision:\n%s", sheet)
	}
}
