// Package store opens the database.
//
// SQLite is the default and lives on the task's own filesystem. That is a
// deliberate choice with consequences (SPEC.md 6): data dies with the task, and
// N tasks means N databases. Postgres stays wired up behind the same interface
// so the production answer is a config change, not a rewrite.
//
// The SQLite driver MUST be the pure-Go one. gorm.io/driver/sqlite pulls in
// mattn/go-sqlite3, which needs CGO, which breaks CGO_ENABLED=0 static builds,
// distroless/static images and ARM64 cross-compilation.
package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Config selects and locates the database.
type Config struct {
	Driver string // "sqlite" (default) or "postgres"
	URL    string // sqlite: file path. postgres: DSN.
	Debug  bool
}

// Open connects and runs migrations for the given models.
func Open(cfg Config, models ...any) (*gorm.DB, error) {
	dialector, err := dialectorFor(cfg)
	if err != nil {
		return nil, err
	}

	logLevel := logger.Warn
	if cfg.Debug {
		logLevel = logger.Info
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		// Without this, gorm.ErrDuplicatedKey is NEVER returned and every
		// "already exists" check silently becomes dead code. This is defect #1
		// in the repo this demo replaces.
		TranslateError: true,
		Logger:         logger.Default.LogMode(logLevel),
	})
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", cfg.Driver, err)
	}

	if err := tune(db, cfg); err != nil {
		return nil, err
	}

	// The error is checked. Ignoring it (defect #9) starts a service against a
	// schema that was never created.
	if len(models) > 0 {
		if err := db.AutoMigrate(models...); err != nil {
			return nil, fmt.Errorf("store: migrate: %w", err)
		}
	}
	return db, nil
}

func dialectorFor(cfg Config) (gorm.Dialector, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Driver)) {
	case "", "sqlite":
		path := cfg.URL
		if path == "" {
			path = "file:data.db"
		}
		// WAL plus a busy timeout: concurrent readers alongside one writer,
		// and a blocked writer waits instead of erroring immediately.
		if !strings.Contains(path, "_pragma") {
			sep := "?"
			if strings.Contains(path, "?") {
				sep = "&"
			}
			path += sep + "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
		}
		return sqlite.Open(path), nil

	case "postgres":
		if cfg.URL == "" {
			return nil, fmt.Errorf("store: DB_URL is required for the postgres driver")
		}
		return postgres.Open(cfg.URL), nil

	default:
		return nil, fmt.Errorf("store: unknown driver %q (want sqlite or postgres)", cfg.Driver)
	}
}

func tune(db *gorm.DB, cfg Config) error {
	pool, err := db.DB()
	if err != nil {
		return fmt.Errorf("store: pool: %w", err)
	}
	if strings.EqualFold(cfg.Driver, "postgres") {
		pool.SetMaxOpenConns(20)
		pool.SetMaxIdleConns(5)
		pool.SetConnMaxLifetime(30 * time.Minute)
		return nil
	}
	// SQLite allows a single writer. More open connections just means lock
	// contention and SQLITE_BUSY, so cap it at one and let the pool queue.
	// ponytail: single connection; raise only if reads measurably starve.
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	return nil
}
