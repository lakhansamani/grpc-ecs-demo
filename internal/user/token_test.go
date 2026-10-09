package user

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func mustIssuer(t *testing.T, secret string) *Issuer {
	t.Helper()
	i, err := NewIssuer(secret)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestIssueThenVerify(t *testing.T) {
	i := mustIssuer(t, "a-test-secret")
	tok, exp, err := i.Issue("user-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if exp <= time.Now().UnixMilli() {
		t.Fatalf("expiry %d is not in the future", exp)
	}
	got, err := i.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	if got != "user-1" {
		t.Fatalf("subject = %q", got)
	}
}

// Every one of these returned ("", nil) or panicked in the old implementation.
func TestVerifyRejectsBadTokens(t *testing.T) {
	i := mustIssuer(t, "a-test-secret")
	for name, tok := range map[string]string{
		"empty":     "",
		"garbage":   "not-a-jwt",
		"two-parts": "aaa.bbb",
	} {
		if _, err := i.Verify(tok); err == nil {
			t.Errorf("%s: want an error, got nil", name)
		}
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	tok, _, err := mustIssuer(t, "secret-one").Issue("user-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mustIssuer(t, "secret-two").Verify(tok); err == nil {
		t.Fatal("a token signed with another secret was accepted")
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	i := mustIssuer(t, "a-test-secret")
	tok, _, err := i.Issue("user-1", time.Now().Add(-2*TokenTTL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.Verify(tok); err == nil {
		t.Fatal("an expired token was accepted")
	}
}

// Algorithm confusion: a token whose alg is "none" must not be accepted.
func TestVerifyRejectsAlgNone(t *testing.T) {
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "attacker",
			Issuer:    "userd",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mustIssuer(t, "a-test-secret").Verify(unsigned); err == nil {
		t.Fatal("alg=none token was accepted - algorithm confusion")
	}
}

func TestShortSecretRejected(t *testing.T) {
	if _, err := NewIssuer("short"); err == nil || !strings.Contains(err.Error(), "8 characters") {
		t.Fatalf("want a length error, got %v", err)
	}
}
