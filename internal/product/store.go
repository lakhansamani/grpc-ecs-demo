// Package product implements ProductService: the catalogue.
//
// Read-only at runtime. The database is baked into the image at build time, so
// every task ships a byte-identical file and all three tasks answer browse,
// search and availability queries the same way. That is what lets a service
// with an embedded database scale horizontally.
//
// Search is SQLite FTS5 - real full-text matching with relevance ranking, in
// pure Go with CGO disabled. No Elasticsearch, and the image stays distroless.
package product

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	productv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/product/v1"
)

// Product is a catalogue entry.
type Product struct {
	ID         string `gorm:"primaryKey"`
	SKU        string `gorm:"uniqueIndex;not null"`
	Title      string `gorm:"not null"`
	Brand      string
	Category   string `gorm:"index"`
	PriceMinor int64  `gorm:"not null"`
	Currency   string `gorm:"not null"`
	Stock      int32  `gorm:"not null"`
}

// ErrNotFound is returned when no product has that id.
var ErrNotFound = errors.New("product: not found")

// Store reads the catalogue.
type Store struct{ db *gorm.DB }

func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// Models is what the migrator needs to know about.
func Models() []any { return []any{&Product{}} }

// BuildSearchIndex creates the FTS5 index and fills it from the products table.
// Called by the seeder at image build time, never at runtime.
func BuildSearchIndex(db *gorm.DB) error {
	stmts := []string{
		`DROP TABLE IF EXISTS product_search`,
		// `content=` makes this an external-content index: the text is not
		// duplicated, FTS5 reads it back from products via rowid.
		`CREATE VIRTUAL TABLE product_search USING fts5(
			title, brand, category, product_id UNINDEXED, tokenize='porter unicode61'
		)`,
		`INSERT INTO product_search(title, brand, category, product_id)
			SELECT title, brand, category, id FROM products`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			return fmt.Errorf("product: build search index: %w", err)
		}
	}
	return nil
}

// List returns a page of the catalogue, optionally filtered by category.
func (s *Store) List(ctx context.Context, category string, limit, offset int) ([]Product, error) {
	q := s.db.WithContext(ctx).Model(&Product{}).Order("title asc")
	if strings.TrimSpace(category) != "" {
		q = q.Where("category = ?", category)
	}
	var out []Product
	if err := q.Limit(limit).Offset(offset).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("product: list: %w", err)
	}
	return out, nil
}

// Get returns one product.
func (s *Store) Get(ctx context.Context, id string) (*Product, error) {
	var p Product
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("product: get: %w", err)
	}
	return &p, nil
}

// Search runs a ranked full-text query and returns the page plus the total
// number of matches.
func (s *Store) Search(ctx context.Context, query string, limit int) ([]Product, int, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, 0, nil
	}

	var total int64
	if err := s.db.WithContext(ctx).
		Raw(`SELECT count(*) FROM product_search WHERE product_search MATCH ?`, match).
		Scan(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("product: search count: %w", err)
	}

	// ORDER BY rank is FTS5's relevance ordering (bm25), best first.
	var out []Product
	if err := s.db.WithContext(ctx).Raw(`
		SELECT p.* FROM product_search ps
		JOIN products p ON p.id = ps.product_id
		WHERE product_search MATCH ?
		ORDER BY rank
		LIMIT ?`, match, limit).Scan(&out).Error; err != nil {
		return nil, 0, fmt.Errorf("product: search: %w", err)
	}
	return out, int(total), nil
}

// ByIDs fetches several products at once, for availability checks.
func (s *Store) ByIDs(ctx context.Context, ids []string) (map[string]Product, error) {
	if len(ids) == 0 {
		return map[string]Product{}, nil
	}
	var rows []Product
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("product: by ids: %w", err)
	}
	out := make(map[string]Product, len(rows))
	for _, p := range rows {
		out[p.ID] = p
	}
	return out, nil
}

// ftsQuery turns user input into a safe FTS5 MATCH expression.
//
// FTS5 has its own query syntax, so raw input would let a user write operators
// - or crash the query with an unbalanced quote. Each word is quoted as a
// literal and the last one gets a prefix `*` so "head" finds "headphones".
func ftsQuery(raw string) string {
	var terms []string
	for _, w := range strings.Fields(strings.ToLower(raw)) {
		var b strings.Builder
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		if b.Len() > 0 {
			terms = append(terms, b.String())
		}
	}
	if len(terms) == 0 {
		return ""
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		if i == len(terms)-1 {
			quoted[i] = `"` + t + `"*` // prefix-match the last word
		} else {
			quoted[i] = `"` + t + `"`
		}
	}
	return strings.Join(quoted, " AND ")
}

// AsProto converts to the wire type.
func (p *Product) AsProto() *productv1.Product {
	return &productv1.Product{
		Id:         p.ID,
		Sku:        p.SKU,
		Title:      p.Title,
		Brand:      p.Brand,
		Category:   p.Category,
		PriceMinor: p.PriceMinor,
		Currency:   p.Currency,
		Stock:      p.Stock,
	}
}
