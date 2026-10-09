package order

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	orderv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/order/v1"
	productv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/product/v1"
	userv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/user/v1"
	"github.com/lakhansamani/grpc-ecs-demo/internal/platform/store"
)

// --- fake upstreams: the point is to test orderd's logic, not the network ---

type fakeUsers struct {
	userv1.UserServiceClient
	id  string
	err error
}

func (f fakeUsers) VerifyToken(context.Context, *userv1.VerifyTokenRequest, ...grpc.CallOption) (*userv1.VerifyTokenResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &userv1.VerifyTokenResponse{User: &userv1.User{Id: f.id}}, nil
}

type fakeProducts struct {
	productv1.ProductServiceClient
	resp  *productv1.CheckAvailabilityResponse
	err   error
	calls int
}

func (f *fakeProducts) CheckAvailability(context.Context, *productv1.CheckAvailabilityRequest, ...grpc.CallOption) (*productv1.CheckAvailabilityResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func ok(id, title string, price int64, stock int32) *productv1.AvailabilityResult {
	return &productv1.AvailabilityResult{
		ProductId: id, Title: title, UnitPriceMinor: price, Currency: "INR",
		StockRemaining: stock, Availability: productv1.Availability_AVAILABILITY_OK,
	}
}

func newSvc(t *testing.T, users userv1.UserServiceClient, products productv1.ProductServiceClient) *Service {
	t.Helper()
	db, err := store.Open(
		store.Config{Driver: "sqlite", URL: "file:" + filepath.Join(t.TempDir(), "o.db")},
		Models()...)
	if err != nil {
		t.Fatal(err)
	}
	return NewService(NewStore(db), users, products,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func authCtx() context.Context {
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer token"))
}

// --- the money test ---

// The total must be computed from the CATALOGUE's prices. CreateOrderRequest
// carries no price field at all, so a client cannot influence what it pays -
// this test is what keeps that true.
func TestTotalUsesCataloguePrices(t *testing.T) {
	products := &fakeProducts{resp: &productv1.CheckAvailabilityResponse{
		AllAvailable: true,
		Results: []*productv1.AvailabilityResult{
			ok("p1", "Headphones", 2999900, 10),
			ok("p2", "Band", 349900, 50),
		},
	}}
	svc := newSvc(t, fakeUsers{id: "u1"}, products)

	resp, err := svc.CreateOrder(authCtx(), &orderv1.CreateOrderRequest{
		Items: []*orderv1.RequestedItem{
			{ProductId: "p1", Quantity: 1},
			{ProductId: "p2", Quantity: 2},
		},
		IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatal(err)
	}
	o := resp.GetOrder()

	// 2999900*1 + 349900*2
	const want = 2999900 + 349900*2
	if o.GetTotalMinor() != want {
		t.Fatalf("total = %d, want %d", o.GetTotalMinor(), want)
	}
	if o.GetStatus() != orderv1.OrderStatus_ORDER_STATUS_CONFIRMED {
		t.Fatalf("status = %v", o.GetStatus())
	}
	for _, l := range o.GetLines() {
		if l.GetLineTotalMinor() != l.GetUnitPriceMinor()*int64(l.GetQuantity()) {
			t.Errorf("line total wrong: %+v", l)
		}
		if l.GetTitle() == "" {
			t.Errorf("title not carried from the catalogue: %+v", l)
		}
	}
	if o.GetUserId() != "u1" {
		t.Fatalf("user id = %q, want the one userd returned", o.GetUserId())
	}
}

func TestRejectionReasons(t *testing.T) {
	cases := map[string]struct {
		results []*productv1.AvailabilityResult
		want    orderv1.RejectionReason
	}{
		"out of stock": {
			[]*productv1.AvailabilityResult{{ProductId: "p1", Availability: productv1.Availability_AVAILABILITY_OUT_OF_STOCK}},
			orderv1.RejectionReason_REJECTION_REASON_OUT_OF_STOCK,
		},
		"insufficient": {
			[]*productv1.AvailabilityResult{{ProductId: "p1", Availability: productv1.Availability_AVAILABILITY_INSUFFICIENT_STOCK}},
			orderv1.RejectionReason_REJECTION_REASON_INSUFFICIENT_STOCK,
		},
		"not found": {
			[]*productv1.AvailabilityResult{{ProductId: "p1", Availability: productv1.Availability_AVAILABILITY_NOT_FOUND}},
			orderv1.RejectionReason_REJECTION_REASON_PRODUCT_NOT_FOUND,
		},
		// A wrong product id is a client bug and outranks a transient stock
		// problem, so the customer gets the actionable reason.
		"not found outranks stock": {
			[]*productv1.AvailabilityResult{
				{ProductId: "p1", Availability: productv1.Availability_AVAILABILITY_OUT_OF_STOCK},
				{ProductId: "p2", Availability: productv1.Availability_AVAILABILITY_NOT_FOUND},
			},
			orderv1.RejectionReason_REJECTION_REASON_PRODUCT_NOT_FOUND,
		},
	}

	for name, tc := range cases {
		svc := newSvc(t, fakeUsers{id: "u1"},
			&fakeProducts{resp: &productv1.CheckAvailabilityResponse{Results: tc.results}})

		resp, err := svc.CreateOrder(authCtx(), &orderv1.CreateOrderRequest{
			Items:          []*orderv1.RequestedItem{{ProductId: "p1", Quantity: 1}},
			IdempotencyKey: "k-" + name,
		})
		// A rejection is a business outcome: OK, with a REJECTED order.
		if err != nil {
			t.Fatalf("%s: a rejection must not be a transport error: %v", name, err)
		}
		o := resp.GetOrder()
		if o.GetStatus() != orderv1.OrderStatus_ORDER_STATUS_REJECTED {
			t.Errorf("%s: status = %v, want REJECTED", name, o.GetStatus())
		}
		if o.GetRejectionReason() != tc.want {
			t.Errorf("%s: reason = %v, want %v", name, o.GetRejectionReason(), tc.want)
		}
		if o.GetTotalMinor() != 0 {
			t.Errorf("%s: a rejected order must not have a total", name)
		}
	}
}

// Retries must not place a second order, and must not re-ask the catalogue.
func TestIdempotentReplay(t *testing.T) {
	products := &fakeProducts{resp: &productv1.CheckAvailabilityResponse{
		AllAvailable: true,
		Results:      []*productv1.AvailabilityResult{ok("p1", "Headphones", 1000, 5)},
	}}
	svc := newSvc(t, fakeUsers{id: "u1"}, products)
	req := &orderv1.CreateOrderRequest{
		Items:          []*orderv1.RequestedItem{{ProductId: "p1", Quantity: 1}},
		IdempotencyKey: "same",
	}

	first, err := svc.CreateOrder(authCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateOrder(authCtx(), req)
	if err != nil {
		t.Fatal(err)
	}

	if first.GetOrder().GetId() != second.GetOrder().GetId() {
		t.Fatal("a repeated idempotency key created a second order")
	}
	if !second.GetIdempotentReplay() {
		t.Error("idempotent_replay should be true on the second call")
	}
	if products.calls != 1 {
		t.Errorf("catalogue called %d times, want 1 - a replay must short-circuit", products.calls)
	}
}

// Two users may legitimately pick the same client-side key.
func TestIdempotencyKeyIsScopedPerUser(t *testing.T) {
	mk := func(uid string) *Service {
		return newSvc(t, fakeUsers{id: uid}, &fakeProducts{resp: &productv1.CheckAvailabilityResponse{
			AllAvailable: true,
			Results:      []*productv1.AvailabilityResult{ok("p1", "X", 1000, 5)},
		}})
	}
	req := &orderv1.CreateOrderRequest{
		Items:          []*orderv1.RequestedItem{{ProductId: "p1", Quantity: 1}},
		IdempotencyKey: "cart-1",
	}
	if _, err := mk("userA").CreateOrder(authCtx(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := mk("userB").CreateOrder(authCtx(), req); err != nil {
		t.Fatalf("a second user reusing the same key must still succeed: %v", err)
	}
}

func TestAuthFailures(t *testing.T) {
	req := &orderv1.CreateOrderRequest{
		Items:          []*orderv1.RequestedItem{{ProductId: "p1", Quantity: 1}},
		IdempotencyKey: "k",
	}

	// No metadata at all.
	svc := newSvc(t, fakeUsers{id: "u1"}, &fakeProducts{})
	if _, err := svc.CreateOrder(context.Background(), req); status.Code(err) != codes.Unauthenticated {
		t.Errorf("no metadata: code = %v, want Unauthenticated", status.Code(err))
	}

	// userd says the token is bad -> pass it through unchanged.
	svc = newSvc(t, fakeUsers{err: status.Error(codes.Unauthenticated, "invalid token")}, &fakeProducts{})
	if _, err := svc.CreateOrder(authCtx(), req); status.Code(err) != codes.Unauthenticated {
		t.Errorf("bad token: code = %v, want Unauthenticated", status.Code(err))
	}

	// userd is down -> Unavailable, NOT Unauthenticated. Conflating these
	// makes an outage look like every user's token expired at once.
	svc = newSvc(t, fakeUsers{err: errors.New("connection refused")}, &fakeProducts{})
	if _, err := svc.CreateOrder(authCtx(), req); status.Code(err) != codes.Unavailable {
		t.Errorf("userd down: code = %v, want Unavailable", status.Code(err))
	}
}

// If the catalogue is unreachable we must not guess at prices.
func TestCatalogueDownIsUnavailable(t *testing.T) {
	svc := newSvc(t, fakeUsers{id: "u1"}, &fakeProducts{err: errors.New("connection refused")})
	_, err := svc.CreateOrder(authCtx(), &orderv1.CreateOrderRequest{
		Items:          []*orderv1.RequestedItem{{ProductId: "p1", Quantity: 1}},
		IdempotencyKey: "k",
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %v, want Unavailable", status.Code(err))
	}
}

func TestCreateOrderValidation(t *testing.T) {
	svc := newSvc(t, fakeUsers{id: "u1"}, &fakeProducts{})
	for name, req := range map[string]*orderv1.CreateOrderRequest{
		"no items": {IdempotencyKey: "k"},
		"no key":   {Items: []*orderv1.RequestedItem{{ProductId: "p1", Quantity: 1}}},
		"blank id": {Items: []*orderv1.RequestedItem{{ProductId: " ", Quantity: 1}}, IdempotencyKey: "k"},
		"zero qty": {Items: []*orderv1.RequestedItem{{ProductId: "p1", Quantity: 0}}, IdempotencyKey: "k"},
		"negative": {Items: []*orderv1.RequestedItem{{ProductId: "p1", Quantity: -1}}, IdempotencyKey: "k"},
	} {
		if _, err := svc.CreateOrder(authCtx(), req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: code = %v, want InvalidArgument", name, status.Code(err))
		}
	}
}

// GetOrder is scoped by owner in the query, so another user's order is
// indistinguishable from one that does not exist.
func TestGetOrderIsScopedToOwner(t *testing.T) {
	products := &fakeProducts{resp: &productv1.CheckAvailabilityResponse{
		AllAvailable: true,
		Results:      []*productv1.AvailabilityResult{ok("p1", "X", 1000, 5)},
	}}
	owner := newSvc(t, fakeUsers{id: "owner"}, products)

	created, err := owner.CreateOrder(authCtx(), &orderv1.CreateOrderRequest{
		Items:          []*orderv1.RequestedItem{{ProductId: "p1", Quantity: 1}},
		IdempotencyKey: "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetOrder().GetId()

	// The owner can read it.
	got, err := owner.GetOrder(authCtx(), &orderv1.GetOrderRequest{Id: id})
	if err != nil {
		t.Fatalf("owner cannot read their own order: %v", err)
	}
	if got.GetOrder().GetId() != id {
		t.Fatal("wrong order returned")
	}

	// A different user gets NotFound, not PermissionDenied - which would leak
	// that the id exists.
	intruder := NewService(owner.store, fakeUsers{id: "someone-else"}, products,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := intruder.GetOrder(authCtx(), &orderv1.GetOrderRequest{Id: id}); status.Code(err) != codes.NotFound {
		t.Fatalf("another user's order: code = %v, want NotFound", status.Code(err))
	}
}

func TestListOrdersOnlyReturnsOwn(t *testing.T) {
	products := &fakeProducts{resp: &productv1.CheckAvailabilityResponse{
		AllAvailable: true,
		Results:      []*productv1.AvailabilityResult{ok("p1", "X", 1000, 5)},
	}}
	svc := newSvc(t, fakeUsers{id: "u1"}, products)
	for _, k := range []string{"a", "b"} {
		if _, err := svc.CreateOrder(authCtx(), &orderv1.CreateOrderRequest{
			Items: []*orderv1.RequestedItem{{ProductId: "p1", Quantity: 1}}, IdempotencyKey: k,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mine, err := svc.ListOrders(authCtx(), &orderv1.ListOrdersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(mine.GetOrders()) != 2 {
		t.Fatalf("got %d orders, want 2", len(mine.GetOrders()))
	}

	other := NewService(svc.store, fakeUsers{id: "u2"}, products,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	theirs, err := other.ListOrders(authCtx(), &orderv1.ListOrdersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(theirs.GetOrders()) != 0 {
		t.Fatalf("another user sees %d of my orders", len(theirs.GetOrders()))
	}
}
