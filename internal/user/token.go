package user

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenTTL is how long a login is good for.
const TokenTTL = 24 * time.Hour

// Issuer mints and verifies JWTs.
//
// Note on the shared secret: with userd scaled to several tasks, every task
// MUST hold the same secret or a token minted by one fails on another, which
// looks exactly like a load-balancing bug. The secret comes from Secrets
// Manager via the task definition, never from the image. See SPEC.md 6.6.
type Issuer struct {
	secret []byte
}

func NewIssuer(secret string) (*Issuer, error) {
	if len(secret) < 8 {
		return nil, fmt.Errorf("identity: JWT secret must be at least 8 characters")
	}
	return &Issuer{secret: []byte(secret)}, nil
}

type claims struct {
	jwt.RegisteredClaims
}

// Issue returns a signed token and its expiry in Unix millis.
func (i *Issuer) Issue(userID string, now time.Time) (string, int64, error) {
	exp := now.Add(TokenTTL)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			Issuer:    "userd",
		},
	})
	signed, err := tok.SignedString(i.secret)
	if err != nil {
		return "", 0, fmt.Errorf("identity: sign token: %w", err)
	}
	return signed, exp.UnixMilli(), nil
}

// Verify checks the signature and expiry and returns the user ID.
//
// The old implementation this replaces had three bugs worth naming on stage:
// it never checked the signing method (so an attacker could swap `alg` -
// algorithm confusion), it returned a nil error alongside an empty user ID on
// invalid tokens, and it type-asserted a claim without checking, so a
// malformed token panicked the server.
func (i *Issuer) Verify(token string) (string, error) {
	parsed, err := jwt.ParseWithClaims(token, &claims{},
		func(t *jwt.Token) (any, error) { return i.secret, nil },
		// Pin the algorithm. Without this, `alg` is attacker-controlled.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("userd"),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return "", fmt.Errorf("identity: invalid token: %w", err)
	}
	c, ok := parsed.Claims.(*claims)
	if !ok || !parsed.Valid {
		return "", fmt.Errorf("identity: invalid token")
	}
	if c.Subject == "" {
		return "", fmt.Errorf("identity: token has no subject")
	}
	return c.Subject, nil
}
