// Package config reads configuration from the environment.
//
// It reports *every* missing or malformed variable in one error rather than
// failing on the first. On ECS a task that dies on boot gives you one shot at
// the logs per deploy cycle, so "DB_URL is required" followed three minutes
// later by "JWT_SECRET is required" is a genuinely expensive way to work.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Loader accumulates lookup errors so all of them surface together.
type Loader struct {
	missing []string
	bad     []string
}

func New() *Loader { return &Loader{} }

// Required returns the value of key, recording an error if it is unset or blank.
func (l *Loader) Required(key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		l.missing = append(l.missing, key)
	}
	return v
}

// Optional returns the value of key, or def if unset.
func (l *Loader) Optional(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// Duration parses key as a Go duration, falling back to def when unset.
func (l *Loader) Duration(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		l.bad = append(l.bad, fmt.Sprintf("%s=%q (want a duration like 30s)", key, raw))
		return def
	}
	return d
}

// Int parses key as an integer, falling back to def when unset.
func (l *Loader) Int(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		l.bad = append(l.bad, fmt.Sprintf("%s=%q (want an integer)", key, raw))
		return def
	}
	return n
}

// Err returns a single error describing everything that was wrong, or nil.
func (l *Loader) Err() error {
	if len(l.missing) == 0 && len(l.bad) == 0 {
		return nil
	}
	var parts []string
	if len(l.missing) > 0 {
		parts = append(parts, "missing required env: "+strings.Join(l.missing, ", "))
	}
	if len(l.bad) > 0 {
		parts = append(parts, "malformed env: "+strings.Join(l.bad, "; "))
	}
	return fmt.Errorf("%s", strings.Join(parts, " | "))
}
