package product

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	productv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/product/v1"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	s, _ := newTestStore(t)
	return NewService(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// CheckAvailability is the authoritative source of price and stock, so each
// verdict has to be exactly right - orderd trusts it completely.
func TestCheckAvailabilityVerdicts(t *testing.T) {
	svc := newTestService(t)

	resp, err := svc.CheckAvailability(context.Background(), &productv1.CheckAvailabilityRequest{
		Items: []*productv1.CartItem{
			{ProductId: "a", Quantity: 2},    // stock 42 -> ok
			{ProductId: "c", Quantity: 1},    // stock 0  -> out of stock
			{ProductId: "d", Quantity: 3},    // stock 1  -> insufficient
			{ProductId: "nope", Quantity: 1}, // missing  -> not found
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetAllAvailable() {
		t.Fatal("all_available must be false when any item fails")
	}

	want := map[string]productv1.Availability{
		"a":    productv1.Availability_AVAILABILITY_OK,
		"c":    productv1.Availability_AVAILABILITY_OUT_OF_STOCK,
		"d":    productv1.Availability_AVAILABILITY_INSUFFICIENT_STOCK,
		"nope": productv1.Availability_AVAILABILITY_NOT_FOUND,
	}
	if len(resp.GetResults()) != len(want) {
		t.Fatalf("got %d results, want %d - one per requested item", len(resp.GetResults()), len(want))
	}
	for _, r := range resp.GetResults() {
		if got := r.GetAvailability(); got != want[r.GetProductId()] {
			t.Errorf("%s: availability = %v, want %v", r.GetProductId(), got, want[r.GetProductId()])
		}
	}
}

// Exactly-enough stock must be allowed. An off-by-one here silently rejects
// the last unit of everything.
func TestCheckAvailabilityExactStockIsOK(t *testing.T) {
	svc := newTestService(t)
	resp, err := svc.CheckAvailability(context.Background(), &productv1.CheckAvailabilityRequest{
		Items: []*productv1.CartItem{{ProductId: "d", Quantity: 1}}, // stock is exactly 1
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetAllAvailable() {
		t.Fatalf("asking for the last unit must be allowed, got %v", resp.GetResults()[0].GetAvailability())
	}
}

// The price orderd uses comes from here, so it must be the catalogue's.
func TestCheckAvailabilityReturnsAuthoritativePrice(t *testing.T) {
	svc := newTestService(t)
	resp, err := svc.CheckAvailability(context.Background(), &productv1.CheckAvailabilityRequest{
		Items: []*productv1.CartItem{{ProductId: "a", Quantity: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := resp.GetResults()[0]
	if r.GetUnitPriceMinor() != 2999900 {
		t.Fatalf("unit price = %d, want the catalogue's 2999900", r.GetUnitPriceMinor())
	}
	if r.GetCurrency() != "INR" || r.GetTitle() == "" {
		t.Fatalf("currency/title not populated: %+v", r)
	}
}

func TestCheckAvailabilityRejectsBadInput(t *testing.T) {
	svc := newTestService(t)
	for name, req := range map[string]*productv1.CheckAvailabilityRequest{
		"no items":      {},
		"blank id":      {Items: []*productv1.CartItem{{ProductId: "  ", Quantity: 1}}},
		"zero quantity": {Items: []*productv1.CartItem{{ProductId: "a", Quantity: 0}}},
		"negative":      {Items: []*productv1.CartItem{{ProductId: "a", Quantity: -3}}},
	} {
		_, err := svc.CheckAvailability(context.Background(), req)
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: code = %v, want InvalidArgument", name, status.Code(err))
		}
	}
}

func TestSearchRequiresQuery(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.SearchProducts(context.Background(), &productv1.SearchProductsRequest{Query: "   "})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

// No matches is a valid answer, not an error.
func TestSearchNoMatchesIsNotAnError(t *testing.T) {
	svc := newTestService(t)
	resp, err := svc.SearchProducts(context.Background(), &productv1.SearchProductsRequest{Query: "refrigerator"})
	if err != nil {
		t.Fatalf("empty result set should not error: %v", err)
	}
	if len(resp.GetProducts()) != 0 || resp.GetTotalMatches() != 0 {
		t.Fatal("expected an empty result set")
	}
}

func TestGetProductNotFound(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.GetProduct(context.Background(), &productv1.GetProductRequest{Id: "nope"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

// page_size must be clamped, or a caller can ask for the whole catalogue.
func TestPageSizeIsClamped(t *testing.T) {
	if got := pageSize(0); got != 20 {
		t.Errorf("pageSize(0) = %d, want the default 20", got)
	}
	if got := pageSize(9999); got != MaxPageSize {
		t.Errorf("pageSize(9999) = %d, want clamped to %d", got, MaxPageSize)
	}
	if got := pageSize(5); got != 5 {
		t.Errorf("pageSize(5) = %d", got)
	}
}

func TestListProductsPaginates(t *testing.T) {
	svc := newTestService(t)
	first, err := svc.ListProducts(context.Background(), &productv1.ListProductsRequest{PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.GetProducts()) != 2 || first.GetNextPageToken() == "" {
		t.Fatalf("want 2 products and a next token, got %d / %q",
			len(first.GetProducts()), first.GetNextPageToken())
	}
	second, err := svc.ListProducts(context.Background(), &productv1.ListProductsRequest{
		PageSize: 2, PageToken: first.GetNextPageToken(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.GetNextPageToken() != "" {
		t.Fatalf("last page should have no next token, got %q", second.GetNextPageToken())
	}
	// Pages must not overlap.
	if first.GetProducts()[0].GetId() == second.GetProducts()[0].GetId() {
		t.Fatal("page 2 repeats page 1")
	}
}

func TestListProductsRejectsBadToken(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.ListProducts(context.Background(), &productv1.ListProductsRequest{PageToken: "abc"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}
