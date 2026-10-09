// Package order implements OrderService: the only service here that writes.
package order

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	orderv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/order/v1"
)

// Order is a placed order, confirmed or rejected. Rejected orders are stored
// too: a customer asking "why didn't my order go through?" needs an answer.
type Order struct {
	ID              string `gorm:"primaryKey"`
	UserID          string `gorm:"index:idx_user_created;not null"`
	TotalMinor      int64  `gorm:"not null"`
	Currency        string `gorm:"not null"`
	Status          int32  `gorm:"not null"`
	RejectionReason int32
	// Unique, so a retried request cannot place a second order. Namespaced
	// per user by the service.
	IdempotencyKey string `gorm:"uniqueIndex"`
	CreatedAt      int64  `gorm:"index:idx_user_created;autoCreateTime:milli"`

	Lines []OrderLine `gorm:"foreignKey:OrderID;constraint:OnDelete:CASCADE"`
}

// OrderLine is one item. UnitPriceMinor is what ProductService said it costs,
// never what the client sent.
type OrderLine struct {
	ID             uint   `gorm:"primaryKey;autoIncrement"`
	OrderID        string `gorm:"index;not null"`
	ProductID      string `gorm:"not null"`
	Title          string
	Quantity       int32 `gorm:"not null"`
	UnitPriceMinor int64 `gorm:"not null"`
	LineTotalMinor int64 `gorm:"not null"`
}

// ErrNotFound is returned when an order does not exist or is not the caller's.
var ErrNotFound = errors.New("order: not found")

// Store persists orders.
type Store struct{ db *gorm.DB }

func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// Models is what the migrator needs to know about.
func Models() []any { return []any{&Order{}, &OrderLine{}} }

// ByIdempotencyKey returns a previously placed order, or ErrNotFound.
func (s *Store) ByIdempotencyKey(ctx context.Context, key string) (*Order, error) {
	var o Order
	err := s.db.WithContext(ctx).Preload("Lines").
		Where("idempotency_key = ?", key).First(&o).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("order: idempotency lookup: %w", err)
	}
	return &o, nil
}

// Create writes the order and its lines in one transaction. A duplicate
// idempotency key surfaces as gorm.ErrDuplicatedKey so the caller can fall
// back to the stored row - which closes the race where two concurrent retries
// both pass the pre-check.
func (s *Store) Create(ctx context.Context, o *Order) error {
	if o.ID == "" {
		o.ID = uuid.NewString()
	}
	if o.CreatedAt == 0 {
		o.CreatedAt = time.Now().UnixMilli()
	}
	for i := range o.Lines {
		o.Lines[i].OrderID = o.ID
	}
	err := s.db.WithContext(ctx).Create(o).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return gorm.ErrDuplicatedKey
	}
	if err != nil {
		return fmt.Errorf("order: create: %w", err)
	}
	return nil
}

// ByID returns an order scoped to its owner. Scoping in the query rather than
// comparing after the read means a wrong owner cannot tell "not yours" from
// "does not exist".
func (s *Store) ByID(ctx context.Context, userID, id string) (*Order, error) {
	var o Order
	err := s.db.WithContext(ctx).Preload("Lines").
		Where("id = ? AND user_id = ?", id, userID).First(&o).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("order: get: %w", err)
	}
	return &o, nil
}

// List returns a user's orders, newest first.
func (s *Store) List(ctx context.Context, userID string, limit, offset int) ([]Order, error) {
	var out []Order
	err := s.db.WithContext(ctx).Preload("Lines").
		Where("user_id = ?", userID).
		Order("created_at desc, id desc").
		Limit(limit).Offset(offset).Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("order: list: %w", err)
	}
	return out, nil
}

// AsProto converts to the wire type.
func (o *Order) AsProto() *orderv1.Order {
	lines := make([]*orderv1.OrderLine, 0, len(o.Lines))
	for _, l := range o.Lines {
		lines = append(lines, &orderv1.OrderLine{
			ProductId:      l.ProductID,
			Title:          l.Title,
			Quantity:       l.Quantity,
			UnitPriceMinor: l.UnitPriceMinor,
			LineTotalMinor: l.LineTotalMinor,
		})
	}
	return &orderv1.Order{
		Id:              o.ID,
		UserId:          o.UserID,
		Lines:           lines,
		TotalMinor:      o.TotalMinor,
		Currency:        o.Currency,
		Status:          orderv1.OrderStatus(o.Status),
		RejectionReason: orderv1.RejectionReason(o.RejectionReason),
		CreatedAt:       o.CreatedAt,
	}
}
