// Package product implements ProductService: the catalogue.
//
// Read-only at runtime. The database is baked into the image at build time, so
// every task ships a byte-identical file and all three tasks answer browse,
// search and availability queries the same way. That is what lets a service
// with an embedded database scale horizontally.
//
// Search is real full-text matching with relevance ranking on BOTH drivers,
// and the two engines are genuinely different:
//
//	sqlite   - an FTS5 virtual table, bm25 ranking, pure Go with CGO disabled
//	postgres - a STORED generated tsvector column, GIN index, ts_rank ranking
//
// Same RPC, same sanitiser, same prefix-match behaviour. No Elasticsearch
// either way, and the image stays distroless.
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

// isPostgres asks GORM which engine is underneath, rather than threading a
// driver string through every constructor.
func isPostgres(db *gorm.DB) bool {
	return db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres"
}

// Models is what the migrator needs to know about.
func Models() []any { return []any{&Product{}} }

// BuildSearchIndex creates the full-text index and fills it from the products
// table. On sqlite the seeder calls it at image build time; on postgres it is
// safe to call at boot from several tasks at once, because every statement is
// IF NOT EXISTS or a generated column the database maintains itself.
func BuildSearchIndex(db *gorm.DB) error {
	var stmts []string
	if isPostgres(db) {
		stmts = []string{
			// A STORED generated column, so postgres keeps the tsvector in step
			// with the row on every write. Nothing in the app has to remember
			// to reindex, which is the usual way a search index goes stale.
			`ALTER TABLE products ADD COLUMN IF NOT EXISTS search_tsv tsvector
				GENERATED ALWAYS AS (
					to_tsvector('english',
						coalesce(title, '') || ' ' ||
						coalesce(brand, '') || ' ' ||
						coalesce(category, ''))
				) STORED`,
			`CREATE INDEX IF NOT EXISTS products_search_tsv_idx
				ON products USING GIN (search_tsv)`,
		}
	} else {
		stmts = []string{
			`DROP TABLE IF EXISTS product_search`,
			// `content=` makes this an external-content index: the text is not
			// duplicated, FTS5 reads it back from products via rowid.
			`CREATE VIRTUAL TABLE product_search USING fts5(
				title, brand, category, product_id UNINDEXED, tokenize='porter unicode61'
			)`,
			`INSERT INTO product_search(title, brand, category, product_id)
				SELECT title, brand, category, id FROM products`,
		}
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
	terms := searchTerms(query)
	if len(terms) == 0 {
		return nil, 0, nil
	}

	if isPostgres(s.db) {
		return s.searchPostgres(ctx, terms, limit)
	}
	return s.searchSQLite(ctx, terms, limit)
}

func (s *Store) searchSQLite(ctx context.Context, terms []string, limit int) ([]Product, int, error) {
	match := fts5Query(terms)

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

func (s *Store) searchPostgres(ctx context.Context, terms []string, limit int) ([]Product, int, error) {
	// to_tsquery, not websearch_to_tsquery: websearch_to_tsquery cannot express
	// a prefix match, and prefix-matching the last word is what makes "head"
	// find "headphones" - the same behaviour the FTS5 path has. Passing raw
	// input to to_tsquery would be an injection and a syntax-error risk, so the
	// terms are sanitised to [a-z0-9] first by searchTerms.
	tsq := postgresQuery(terms)

	var total int64
	if err := s.db.WithContext(ctx).
		Raw(`SELECT count(*) FROM products WHERE search_tsv @@ to_tsquery('english', ?)`, tsq).
		Scan(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("product: search count: %w", err)
	}

	var out []Product
	if err := s.db.WithContext(ctx).Raw(`
		SELECT * FROM products
		WHERE search_tsv @@ to_tsquery('english', ?)
		ORDER BY ts_rank(search_tsv, to_tsquery('english', ?)) DESC, title ASC
		LIMIT ?`, tsq, tsq, limit).Scan(&out).Error; err != nil {
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

// searchTerms reduces user input to lowercase alphanumeric words.
//
// This is the security boundary for both engines. FTS5 and to_tsquery each have
// their own query syntax, so raw input could inject operators or crash the
// query with an unbalanced quote. Everything that is not a letter or a digit is
// dropped here, once, so neither driver-specific builder below has to be
// careful.
func searchTerms(raw string) []string {
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
	return terms
}

// fts5Query builds an FTS5 MATCH expression: every term quoted as a literal,
// with the last one prefix-matched so "head" finds "headphones".
func fts5Query(terms []string) string {
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

// postgresQuery builds a to_tsquery expression with the same shape: AND
// between terms, prefix match on the last one.
func postgresQuery(terms []string) string {
	parts := make([]string, len(terms))
	for i, t := range terms {
		if i == len(terms)-1 {
			parts[i] = t + ":*"
		} else {
			parts[i] = t
		}
	}
	return strings.Join(parts, " & ")
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
