package store

import (
	"net/url"
	"os"
	"sync"
	"testing"
)

func TestSplitDSN(t *testing.T) {
	for _, tc := range []struct{ in, wantTarget, wantAdmin string }{
		{"postgres://u:p@host:5432/userd?sslmode=require", "userd", "postgres://u:p@host:5432/postgres?sslmode=require"},
		{"postgresql://u:p@host/orderd", "orderd", "postgresql://u:p@host/postgres"},
		{"postgres://u:p@host:5432/postgres", "postgres", "postgres://u:p@host:5432/postgres"},
	} {
		target, admin, err := splitDSN(tc.in)
		if err != nil {
			t.Fatalf("splitDSN(%q): %v", tc.in, err)
		}
		if target != tc.wantTarget || admin != tc.wantAdmin {
			t.Errorf("splitDSN(%q) = (%q, %q), want (%q, %q)", tc.in, target, admin, tc.wantTarget, tc.wantAdmin)
		}
	}
	if _, _, err := splitDSN("mysql://u:p@host/db"); err == nil {
		t.Error("want an error for a non-postgres scheme")
	}
}

// A password with URL-significant characters must survive the round trip,
// otherwise the DSN silently points somewhere else.
func TestSplitDSNKeepsEncodedPassword(t *testing.T) {
	in := "postgres://ecom_app:p%40ss-w_rd@db.example.com:5432/productsd?sslmode=require"
	target, admin, err := splitDSN(in)
	if err != nil {
		t.Fatal(err)
	}
	if target != "productsd" {
		t.Errorf("target = %q, want productsd", target)
	}
	if want := "postgres://ecom_app:p%40ss-w_rd@db.example.com:5432/postgres?sslmode=require"; admin != want {
		t.Errorf("admin = %q, want %q", admin, want)
	}
}

func TestQuoteIdent(t *testing.T) {
	for in, want := range map[string]string{
		"userd":   `"userd"`,
		`we"ird`:  `"we""ird"`,
		"drop me": `"drop me"`,
	} {
		if got := quoteIdent(in); got != want {
			t.Errorf("quoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnsureDatabaseIsNoopForSQLite(t *testing.T) {
	if err := EnsureDatabase(Config{Driver: "sqlite", URL: "file:x.db"}); err != nil {
		t.Errorf("sqlite should be a no-op, got %v", err)
	}
	if err := EnsureDatabase(Config{URL: "file:x.db"}); err != nil {
		t.Errorf("empty driver should be a no-op, got %v", err)
	}
}

// Needs a real server. Set POSTGRES_TEST_URL (pointing at any database on the
// instance) to run it:
//
//	POSTGRES_TEST_URL="postgres://postgres:test@localhost:55432/postgres?sslmode=disable" \
//	  go test ./internal/platform/store/ -run EnsureDatabaseCreates -v
func TestEnsureDatabaseCreatesAndIsIdempotent(t *testing.T) {
	base := os.Getenv("POSTGRES_TEST_URL")
	if base == "" {
		t.Skip("set POSTGRES_TEST_URL to run the postgres database-creation test")
	}

	// Build each DSN with net/url rather than string surgery on the query.
	dsnFor := func(t *testing.T, name string) string {
		t.Helper()
		u, err := url.Parse(base)
		if err != nil {
			t.Fatal(err)
		}
		u.Path = "/" + name
		return u.String()
	}

	for _, name := range []string{"svc_alpha", "svc_beta"} {
		cfg := Config{Driver: "postgres", URL: dsnFor(t, name)}

		// First call creates it; the second must be a silent no-op.
		for i := 0; i < 2; i++ {
			if err := EnsureDatabase(cfg); err != nil {
				t.Fatalf("EnsureDatabase(%s) pass %d: %v", name, i, err)
			}
		}
		// And it must actually be usable.
		db, err := Open(cfg)
		if err != nil {
			t.Fatalf("Open(%s): %v", name, err)
		}
		if pool, err := db.DB(); err == nil {
			_ = pool.Close()
		}
	}

	// Several tasks boot at once. Every one must succeed; the losers of the
	// race see duplicate_database and must treat it as success.
	cfg := Config{Driver: "postgres", URL: dsnFor(t, "svc_racy")}
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = EnsureDatabase(cfg) }(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent EnsureDatabase #%d: %v", i, err)
		}
	}
}
