package store

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// maintenanceDB is the database that always exists on a fresh Postgres
// instance, and the only one we can be sure of connecting to in order to
// create another.
const maintenanceDB = "postgres"

// EnsureDatabase creates the database named in a Postgres DSN if it is not
// there yet, and does nothing for SQLite.
//
// WHY THIS EXISTS: each service gets its OWN database on ONE shared RDS
// instance — logical isolation between services, a single instance to pay for.
// But `CREATE DATABASE` is SQL, and the AWS Terraform provider only speaks the
// AWS API, so Terraform cannot create them. The alternatives were a second
// Terraform provider that must reach the instance over the network (it cannot;
// the database is not publicly accessible) or a one-off migration task (another
// moving part on stage). So each service creates its own at boot.
//
// Safe to call concurrently. Three userd tasks starting together will race, and
// the losers get SQLSTATE 42P04 (duplicate_database), which is treated as
// success — the only outcome that matters is that the database exists
// afterwards.
//
// It deliberately does NOT create roles or grant anything: every service
// connects as the same master user. Separate users per service would be the
// next step in a real deployment, and is the thing to say out loud rather than
// pretend this is complete.
func EnsureDatabase(cfg Config) error {
	if !strings.EqualFold(strings.TrimSpace(cfg.Driver), "postgres") {
		return nil
	}

	target, adminDSN, err := splitDSN(cfg.URL)
	if err != nil {
		return err
	}
	if target == "" || target == maintenanceDB {
		return nil // nothing to create
	}

	admin, err := gorm.Open(postgres.Open(adminDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return fmt.Errorf("store: connect to %q to create %q: %w", maintenanceDB, target, err)
	}
	defer func() {
		if pool, err := admin.DB(); err == nil {
			_ = pool.Close()
		}
	}()

	var exists bool
	if err := admin.Raw(
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = ?)`, target,
	).Scan(&exists).Error; err != nil {
		return fmt.Errorf("store: look up database %q: %w", target, err)
	}
	if exists {
		return nil
	}

	// CREATE DATABASE cannot be parameterised, and Postgres has no
	// IF NOT EXISTS for it. The name comes from our own Terraform, not from a
	// request, but quote it anyway so a name needing quoting cannot break the
	// statement or smuggle in more SQL.
	stmt := fmt.Sprintf("CREATE DATABASE %s", quoteIdent(target))
	if err := admin.Exec(stmt).Error; err != nil {
		if isDuplicateDatabase(err) {
			return nil // another task won the race; that is a success
		}
		return fmt.Errorf("store: create database %q: %w", target, err)
	}
	return nil
}

// splitDSN returns the database name a DSN points at, plus the same DSN
// rewritten to point at the maintenance database.
func splitDSN(dsn string) (target, adminDSN string, err error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", "", fmt.Errorf("store: parse DB_URL: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", "", fmt.Errorf("store: DB_URL must be a postgres:// URL, got %q", u.Scheme)
	}
	target = strings.TrimPrefix(u.Path, "/")

	admin := *u
	admin.Path = "/" + maintenanceDB
	return target, admin.String(), nil
}

// quoteIdent wraps a Postgres identifier in double quotes, doubling any quote
// inside it.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// isDuplicateDatabase reports whether err means "somebody else already made
// it", which is a success for our purposes.
//
// TWO codes, and the second one is easy to miss — a concurrency test caught it
// here:
//
//	42P04  duplicate_database  - the database already existed when CREATE ran
//	23505  unique_violation    - two CREATE DATABASE statements raced, and this
//	                             one lost on pg_database_datname_index
//
// Only checking 42P04 looks correct and passes every sequential test, then
// fails exactly when three tasks boot together — which is the only time it
// matters.
//
// Matched on the code, not the message, so it survives a reworded or localised
// server error. Same reason the services return enums instead of strings.
func isDuplicateDatabase(err error) bool {
	var pg interface{ SQLState() string }
	if errors.As(err, &pg) {
		switch pg.SQLState() {
		case "42P04", "23505":
			return true
		default:
			return false
		}
	}
	// Last resort if the driver does not expose SQLSTATE.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "pg_database_datname_index")
}
