package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// User is the persisted user. PasswordHash is never a plaintext password and
// never leaves this package.
type User struct {
	ID           string `gorm:"primaryKey"`
	Name         string `gorm:"not null"`
	Email        string `gorm:"uniqueIndex;not null"`
	PasswordHash string `gorm:"not null"`
	CreatedAt    int64  `gorm:"autoCreateTime:milli"`
}

// ErrEmailTaken is returned when the unique index on email rejects an insert.
var ErrEmailTaken = errors.New("identity: email already registered")

// Store is the persistence boundary for users.
type Store struct{ db *gorm.DB }

func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// Models is what the migrator needs to know about.
func Models() []any { return []any{&User{}} }

// Create hashes the password and inserts the user.
//
// Hashing happens HERE, not in a GORM BeforeSave hook. The hook version
// (defect #4) re-bcrypts on every save, so any later update to a user
// double-hashes the stored hash and locks the account out permanently.
func (s *Store) Create(ctx context.Context, name, email, password string) (*User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("identity: hash password: %w", err)
	}
	u := &User{
		ID:           uuid.NewString(),
		Name:         name,
		Email:        email,
		PasswordHash: string(hash),
		CreatedAt:    time.Now().UnixMilli(),
	}
	if err := s.db.WithContext(ctx).Create(u).Error; err != nil {
		// Reachable only because gorm.Config sets TranslateError (defect #1).
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrEmailTaken
		}
		return nil, fmt.Errorf("identity: create user: %w", err)
	}
	return u, nil
}

// ByEmail looks a user up by email. Returns gorm.ErrRecordNotFound if absent.
func (s *Store) ByEmail(ctx context.Context, email string) (*User, error) {
	var u User
	if err := s.db.WithContext(ctx).Where("email = ?", email).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// ByID looks a user up by id. Returns gorm.ErrRecordNotFound if absent.
func (s *Store) ByID(ctx context.Context, id string) (*User, error) {
	var u User
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// CheckPassword reports whether password matches the stored hash.
func (u *User) CheckPassword(password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}
