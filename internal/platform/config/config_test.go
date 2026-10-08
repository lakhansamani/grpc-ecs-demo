package config

import (
	"strings"
	"testing"
	"time"
)

func TestReportsAllProblemsAtOnce(t *testing.T) {
	t.Setenv("PRESENT", "value")
	t.Setenv("BAD_DURATION", "soon")

	l := New()
	l.Required("MISSING_ONE")
	l.Required("MISSING_TWO")
	if got := l.Required("PRESENT"); got != "value" {
		t.Fatalf("Required(PRESENT) = %q", got)
	}
	l.Duration("BAD_DURATION", time.Second)

	err := l.Err()
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	// The whole point: one error naming every problem.
	for _, want := range []string{"MISSING_ONE", "MISSING_TWO", "BAD_DURATION"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %s: %s", want, msg)
		}
	}
	if strings.Contains(msg, "PRESENT") {
		t.Errorf("error mentions a variable that was fine: %s", msg)
	}
}

func TestDefaultsAndNoError(t *testing.T) {
	l := New()
	if got := l.Optional("UNSET_KEY", "fallback"); got != "fallback" {
		t.Fatalf("Optional = %q", got)
	}
	if got := l.Duration("UNSET_KEY", 5*time.Second); got != 5*time.Second {
		t.Fatalf("Duration = %v", got)
	}
	if got := l.Int("UNSET_KEY", 42); got != 42 {
		t.Fatalf("Int = %d", got)
	}
	if err := l.Err(); err != nil {
		t.Fatalf("want no error, got %v", err)
	}
}
