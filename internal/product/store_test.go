package product

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lakhansamani/grpc-ecs-ecom/internal/platform/store"
	"gorm.io/gorm"
)

func newTestStore(t *testing.T) (*Store, *gorm.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := store.Open(store.Config{Driver: "sqlite", URL: "file:" + path}, Models()...)
	if err != nil {
		t.Fatal(err)
	}
	seed := []Product{
		{ID: "a", SKU: "S-A", Title: "Wireless Noise Cancelling Headphones", Brand: "Sony", Category: "audio", PriceMinor: 2999900, Currency: "INR", Stock: 42},
		{ID: "b", SKU: "S-B", Title: "Noise Cancelling Earbuds", Brand: "Bose", Category: "audio", PriceMinor: 2299900, Currency: "INR", Stock: 11},
		{ID: "c", SKU: "S-C", Title: "Running Shoes Mesh", Brand: "Nike", Category: "footwear", PriceMinor: 1199500, Currency: "INR", Stock: 0},
		{ID: "d", SKU: "S-D", Title: "Ultraboost Shoes", Brand: "Adidas", Category: "footwear", PriceMinor: 1699900, Currency: "INR", Stock: 1},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := BuildSearchIndex(db); err != nil {
		t.Fatal(err)
	}
	return NewStore(db), db
}

// ftsQuery is the only place user input reaches FTS5, which has its own query
// syntax. If this stops neutralising operators and quotes, a search box
// becomes a query-injection hole.
func TestFTSQuerySanitisesInput(t *testing.T) {
	for in, want := range map[string]string{
		"headphones":       `"headphones"*`,
		"noise cancelling": `"noise" AND "cancelling"*`,
		"  Noise  SHOES  ": `"noise" AND "shoes"*`,
		`"quoted"`:         `"quoted"*`,
		"a OR b":           `"a" AND "or" AND "b"*`, // OR is neutralised to a term
		"shoes*":           `"shoes"*`,
		"NEAR(a b)":        `"neara" AND "b"*`,
		"":                 "",
		"!!! ???":          "",
		"sony-wh1000":      `"sonywh1000"*`,
	} {
		if got := ftsQuery(in); got != want {
			t.Errorf("ftsQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

// A query with only punctuation must return nothing rather than erroring or
// matching everything.
func TestSearchIgnoresGarbageQuery(t *testing.T) {
	s, _ := newTestStore(t)
	rows, total, err := s.Search(context.Background(), "!!! ???", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || total != 0 {
		t.Fatalf("want no results, got %d rows / total %d", len(rows), total)
	}
}

func TestSearchRanksAndCounts(t *testing.T) {
	s, _ := newTestStore(t)
	rows, total, err := s.Search(context.Background(), "cancelling", 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("total=%d rows=%d, want 2 and 2", total, len(rows))
	}
	// Prefix match on the final term: "head" must find "Headphones".
	rows, _, err = s.Search(context.Background(), "head", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "a" {
		t.Fatalf("prefix search did not find the headphones: %+v", rows)
	}
}

// total is the number of matches BEFORE the page limit, which is what a UI
// needs to say "showing 1 of 2".
func TestSearchTotalIgnoresLimit(t *testing.T) {
	s, _ := newTestStore(t)
	rows, total, err := s.Search(context.Background(), "cancelling", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("limit not applied: %d rows", len(rows))
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2 (matches before the limit)", total)
	}
}

func TestListFiltersByCategory(t *testing.T) {
	s, _ := newTestStore(t)
	rows, err := s.List(context.Background(), "footwear", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d footwear rows, want 2", len(rows))
	}
	rows, err = s.List(context.Background(), "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("unfiltered list returned %d, want 4", len(rows))
	}
}

func TestGetMissingReturnsErrNotFound(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Get(context.Background(), "nope"); err == nil {
		t.Fatal("want an error for a missing product")
	} else if err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestByIDsSkipsUnknown(t *testing.T) {
	s, _ := newTestStore(t)
	got, err := s.ByIDs(context.Background(), []string{"a", "nope", "d"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d products, want 2 (unknown ids are simply absent)", len(got))
	}
	if _, ok := got["nope"]; ok {
		t.Fatal("unknown id should not appear in the map")
	}
}
