package payment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	paymentv1 "github.com/lakhansamani/grpc-ecs-payments/gen/go/payment/v1"
	"github.com/lakhansamani/grpc-ecs-payments/internal/payment/explain"
)

// Transaction is a persisted authorization attempt, approved or declined.
// Declines are stored too: they are what the velocity rule counts and what
// ExplainDecision is asked about.
type Transaction struct {
	ID               string `gorm:"primaryKey"`
	UserID           string `gorm:"index:idx_user_created;not null"`
	AmountMinor      int64  `gorm:"not null"`
	Currency         string `gorm:"not null"`
	MerchantID       string
	MerchantCategory string
	Decision         int32 `gorm:"not null"`
	DeclineReason    int32
	// Unique so a retried request cannot authorize twice. Scoped per user by
	// construction (the service prefixes the user id).
	IdempotencyKey string `gorm:"uniqueIndex"`
	CreatedAt      int64  `gorm:"index:idx_user_created;autoCreateTime:milli"`
}

// ErrNotFound is returned when a transaction does not exist or is not the
// caller's.
var ErrNotFound = errors.New("payment: transaction not found")

// Store is the persistence boundary for transactions.
type Store struct{ db *gorm.DB }

func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// Models is what the migrator needs to know about.
func Models() []any { return []any{&Transaction{}} }

// ByIdempotencyKey returns a previously recorded attempt, or ErrNotFound.
func (s *Store) ByIdempotencyKey(ctx context.Context, key string) (*Transaction, error) {
	var t Transaction
	err := s.db.WithContext(ctx).Where("idempotency_key = ?", key).First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("payment: lookup idempotency key: %w", err)
	}
	return &t, nil
}

// Create records an attempt. A duplicate idempotency key surfaces as
// gorm.ErrDuplicatedKey so the caller can fall back to the stored row - this
// closes the race where two concurrent retries both pass the pre-check.
func (s *Store) Create(ctx context.Context, t *Transaction) error {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = time.Now().UnixMilli()
	}
	if err := s.db.WithContext(ctx).Create(t).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return gorm.ErrDuplicatedKey
		}
		return fmt.Errorf("payment: create transaction: %w", err)
	}
	return nil
}

// ByID returns a transaction scoped to its owner. Scoping in the query rather
// than comparing after the read means a wrong owner is indistinguishable from
// a missing row, which avoids leaking that an id exists.
func (s *Store) ByID(ctx context.Context, userID, id string) (*Transaction, error) {
	var t Transaction
	err := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("payment: get transaction: %w", err)
	}
	return &t, nil
}

// Stats returns the number of attempts since `since` and the total approved
// spend for a user - the two aggregates the rules need.
func (s *Store) Stats(ctx context.Context, userID string, since int64) (attempts int, spentMinor int64, err error) {
	var count int64
	if err := s.db.WithContext(ctx).Model(&Transaction{}).
		Where("user_id = ? AND created_at >= ?", userID, since).
		Count(&count).Error; err != nil {
		return 0, 0, fmt.Errorf("payment: count attempts: %w", err)
	}

	var spent *int64
	if err := s.db.WithContext(ctx).Model(&Transaction{}).
		Where("user_id = ? AND decision = ?", userID, int32(paymentv1.Decision_DECISION_APPROVED)).
		Select("sum(amount_minor)").Scan(&spent).Error; err != nil {
		return 0, 0, fmt.Errorf("payment: sum approved: %w", err)
	}
	if spent != nil {
		spentMinor = *spent
	}
	return int(count), spentMinor, nil
}

// List returns a user's transactions newest-first, plus the offset to resume
// from. Keyset pagination would be better; offset is honest for a demo.
// ponytail: offset pagination; switch to keyset if a page ever gets large.
func (s *Store) List(ctx context.Context, userID string, limit, offset int) ([]Transaction, error) {
	var out []Transaction
	err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at desc, id desc").
		Limit(limit).Offset(offset).
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("payment: list transactions: %w", err)
	}
	return out, nil
}

// AsProto converts to the wire type.
func (t *Transaction) AsProto() *paymentv1.Transaction {
	return &paymentv1.Transaction{
		Id:               t.ID,
		UserId:           t.UserID,
		AmountMinor:      t.AmountMinor,
		Currency:         t.Currency,
		MerchantId:       t.MerchantID,
		MerchantCategory: t.MerchantCategory,
		Decision:         paymentv1.Decision(t.Decision),
		DeclineReason:    paymentv1.DeclineReason(t.DeclineReason),
		CreatedAt:        t.CreatedAt,
	}
}

// decisionToProto maps the internal decision onto the wire enum.
func decisionToProto(d explain.Decision) int32 {
	if d == explain.Approved {
		return int32(paymentv1.Decision_DECISION_APPROVED)
	}
	return int32(paymentv1.Decision_DECISION_DECLINED)
}

// reasonToProto maps the internal reason onto the wire enum.
func reasonToProto(r explain.Reason) int32 {
	switch r {
	case explain.ReasonLimitExceeded:
		return int32(paymentv1.DeclineReason_DECLINE_REASON_LIMIT_EXCEEDED)
	case explain.ReasonVelocity:
		return int32(paymentv1.DeclineReason_DECLINE_REASON_VELOCITY)
	case explain.ReasonMerchantBlocked:
		return int32(paymentv1.DeclineReason_DECLINE_REASON_MERCHANT_BLOCKED)
	case explain.ReasonInsufficientFunds:
		return int32(paymentv1.DeclineReason_DECLINE_REASON_INSUFFICIENT_FUNDS)
	default:
		return int32(paymentv1.DeclineReason_DECLINE_REASON_UNSPECIFIED)
	}
}

// reasonFromProto is the inverse, for ExplainDecision on a stored row.
func reasonFromProto(r paymentv1.DeclineReason) explain.Reason {
	switch r {
	case paymentv1.DeclineReason_DECLINE_REASON_LIMIT_EXCEEDED:
		return explain.ReasonLimitExceeded
	case paymentv1.DeclineReason_DECLINE_REASON_VELOCITY:
		return explain.ReasonVelocity
	case paymentv1.DeclineReason_DECLINE_REASON_MERCHANT_BLOCKED:
		return explain.ReasonMerchantBlocked
	case paymentv1.DeclineReason_DECLINE_REASON_INSUFFICIENT_FUNDS:
		return explain.ReasonInsufficientFunds
	default:
		return explain.ReasonNone
	}
}
