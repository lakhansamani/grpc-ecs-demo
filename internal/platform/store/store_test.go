package store

import (
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"
)

type row struct {
	ID    string `gorm:"primaryKey"`
	Email string `gorm:"uniqueIndex"`
}

func TestUnknownDriverIsRejected(t *testing.T) {
	_, err := Open(Config{Driver: "mysql", URL: "x"})
	if err == nil || !strings.Contains(err.Error(), "unknown driver") {
		t.Fatalf("want an unknown-driver error, got %v", err)
	}
}

func TestPostgresRequiresURL(t *testing.T) {
	_, err := Open(Config{Driver: "postgres"})
	if err == nil || !strings.Contains(err.Error(), "DB_URL is required") {
		t.Fatalf("want a missing-URL error, got %v", err)
	}
}

// Without TranslateError, gorm.ErrDuplicatedKey is NEVER returned and every
// "already exists" check in the codebase silently becomes dead code. That was
// a real bug in the repo this demo replaces, so it gets a test.
func TestTranslateErrorIsEnabled(t *testing.T) {
	db := openTemp(t)
	if err := db.Create(&row{ID: "1", Email: "a@example.com"}).Error; err != nil {
		t.Fatal(err)
	}
	err := db.Create(&row{ID: "2", Email: "a@example.com"}).Error
	if err == nil {
		t.Fatal("the unique index did not reject a duplicate")
	}
	if !isDuplicate(err) {
		t.Fatalf("want gorm.ErrDuplicatedKey, got %v - TranslateError is probably off", err)
	}
}

// Migrations must be checked, not ignored: a service that starts against a
// schema that was never created fails in confusing ways later.
func TestMigrationErrorIsReturned(t *testing.T) {
	// A model with no fields cannot be migrated.
	type broken struct{}
	_, err := Open(Config{Driver: "sqlite", URL: "file:" + filepath.Join(t.TempDir(), "b.db")}, &broken{})
	if err == nil {
		t.Fatal("want a migration error for an unmigratable model")
	}
	if !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("error should mention migration: %v", err)
	}
}

// WAL plus a busy timeout is what lets readers run alongside the single
// writer instead of getting SQLITE_BUSY.
func TestSQLitePragmasApplied(t *testing.T) {
	db := openTemp(t)
	var mode string
	if err := db.Raw("PRAGMA journal_mode").Scan(&mode).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	var timeout int
	if err := db.Raw("PRAGMA busy_timeout").Scan(&timeout).Error; err != nil {
		t.Fatal(err)
	}
	if timeout == 0 {
		t.Error("busy_timeout is 0; a blocked writer will error instead of waiting")
	}
}

// SQLite allows one writer, so more open connections just means lock
// contention. The pool is capped at one on purpose.
func TestSQLitePoolIsCappedAtOne(t *testing.T) {
	db := openTemp(t)
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if got := pool.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
}

func openTemp(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := Open(
		Config{Driver: "sqlite", URL: "file:" + filepath.Join(t.TempDir(), "t.db")},
		&row{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func isDuplicate(err error) bool {
	return err != nil && errorsIs(err, gorm.ErrDuplicatedKey)
}

func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
