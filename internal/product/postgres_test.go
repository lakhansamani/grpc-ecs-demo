package product

import (
	"context"
	"os"
	"testing"

	"github.com/lakhansamani/grpc-ecs-demo/internal/platform/store"
)

// The postgres search path cannot be tested with sqlite, and `go test ./...`
// must stay offline, so this runs only when POSTGRES_TEST_URL is set:
//
//	docker run -d --name pgtest -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:16-alpine
//	POSTGRES_TEST_URL="postgres://postgres:test@localhost:55432/postgres?sslmode=disable" \
//	  go test ./internal/product/ -run Postgres -v
func TestPostgresSearch(t *testing.T) {
	url := os.Getenv("POSTGRES_TEST_URL")
	if url == "" {
		t.Skip("set POSTGRES_TEST_URL to run the postgres search test")
	}

	db, err := store.Open(store.Config{Driver: "postgres", URL: url}, Models()...)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`TRUNCATE products`).Error; err != nil {
		t.Fatal(err)
	}
	seed := []Product{
		{ID: "p-1", SKU: "SONY-XM5", Title: "Wireless Noise Cancelling Headphones", Brand: "Sony", Category: "audio", PriceMinor: 2999900, Currency: "INR", Stock: 42},
		{ID: "p-2", SKU: "BOSE-QC", Title: "Noise Cancelling Earbuds Ultra", Brand: "Bose", Category: "audio", PriceMinor: 2299900, Currency: "INR", Stock: 11},
		{ID: "p-3", SKU: "NIKE-PEG", Title: "Running Shoes Lightweight Mesh", Brand: "Nike", Category: "footwear", PriceMinor: 1199500, Currency: "INR", Stock: 7},
	}
	for i := range seed {
		if err := db.Save(&seed[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Safe to call repeatedly and from several tasks at once.
	for i := 0; i < 2; i++ {
		if err := BuildSearchIndex(db); err != nil {
			t.Fatalf("BuildSearchIndex (pass %d): %v", i, err)
		}
	}

	s := NewStore(db)
	if !isPostgres(db) {
		t.Fatal("expected the postgres dialector")
	}
	ctx := context.Background()

	for _, tc := range []struct {
		query string
		want  int
		name  string
	}{
		// "cancelling" must stem to the same lexeme the tsvector holds. This is
		// the exact query the stage demo runs.
		{"cancelling", 2, "the demo query, stemmed"},
		{"noise cancelling", 2, "two terms, ANDed"},
		{"head", 1, "prefix match on the last word"},
		{"shoes", 1, "different category"},
		{"sony", 1, "matches the brand column"},
		{"footwear", 1, "matches the category column"},
		{"nonexistentgibberish", 0, "no match"},
		{"!!! ???", 0, "garbage returns nothing, does not error"},
		{"title:bad", 0, "an injected operator is neutralised to a term"},
	} {
		got, total, err := s.Search(ctx, tc.query, 10)
		if err != nil {
			t.Fatalf("%s: Search(%q): %v", tc.name, tc.query, err)
		}
		if len(got) != tc.want || total != tc.want {
			t.Errorf("%s: Search(%q) = %d rows / total %d, want %d",
				tc.name, tc.query, len(got), total, tc.want)
		}
	}

	// Ranking must put the better match first for a single-term query.
	got, _, err := s.Search(ctx, "headphones", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "p-1" {
		t.Errorf("ranking: got %+v, want p-1 first", got)
	}
}
