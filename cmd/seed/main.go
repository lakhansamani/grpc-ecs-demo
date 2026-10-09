// Command seed creates an identityd SQLite database with demo users.
//
// It runs at IMAGE BUILD TIME, not at boot. That is what makes identityd
// horizontally scalable despite using an embedded database: every task ships a
// byte-identical file, so all read paths (Login, VerifyToken) answer the same
// on every task and the load-balancing demo behaves.
//
// Consequence, stated plainly: Register writes to one task's filesystem and
// does not survive task replacement. Anything run at more than one task must
// authenticate as a seeded user. See SPEC.md 6.4.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/lakhansamani/grpc-ecs-payments/internal/platform/store"
	"github.com/lakhansamani/grpc-ecs-payments/internal/user"
)

// SeedUsers are fixed so the demo is reproducible and the passwords can go on
// a slide. They are demo credentials for a throwaway environment, nothing more.
var SeedUsers = []struct{ Name, Email, Password string }{
	{"Demo User", "demo@example.com", "demo-password"},
	{"Asha Patel", "asha@example.com", "demo-password"},
	{"Ravi Mehta", "ravi@example.com", "demo-password"},
}

func main() {
	dbURL := flag.String("db", "file:/data/user.db", "sqlite database URL")
	flag.Parse()

	if err := run(*dbURL); err != nil {
		log.Fatalf("seed: %v", err)
	}
}

func run(dbURL string) error {
	db, err := store.Open(store.Config{Driver: "sqlite", URL: dbURL}, user.Models()...)
	if err != nil {
		return err
	}
	s := user.NewStore(db)
	ctx := context.Background()

	for _, u := range SeedUsers {
		created, err := s.Create(ctx, u.Name, u.Email, u.Password)
		switch {
		case err == nil:
			fmt.Fprintf(os.Stdout, "seeded %s (%s)\n", u.Email, created.ID)
		case errors.Is(err, user.ErrEmailTaken):
			// Idempotent: re-running the seeder must not fail the build.
			fmt.Fprintf(os.Stdout, "exists %s\n", u.Email)
		default:
			return fmt.Errorf("create %s: %w", u.Email, err)
		}
	}

	// Close the pool so WAL/SHM files are checkpointed before the file is
	// copied into the runtime image. Skipping this can bake in a database with
	// uncommitted WAL contents.
	pool, err := db.DB()
	if err != nil {
		return err
	}
	if err := pool.Close(); err != nil {
		return fmt.Errorf("close pool: %w", err)
	}

	fmt.Fprintf(os.Stdout, "seed complete: %s\n", strings.TrimPrefix(dbURL, "file:"))
	return nil
}
