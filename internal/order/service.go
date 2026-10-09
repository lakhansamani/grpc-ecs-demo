package order

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	orderv1 "github.com/lakhansamani/grpc-ecs-ecom/gen/go/order/v1"
	productv1 "github.com/lakhansamani/grpc-ecs-ecom/gen/go/product/v1"
	userv1 "github.com/lakhansamani/grpc-ecs-ecom/gen/go/user/v1"
)

// MaxPageSize caps ListOrders.
const MaxPageSize = 100

// Service implements orderv1.OrderServiceServer.
//
// It makes TWO outbound gRPC calls on every CreateOrder:
//
//	UserService    - who is asking (it holds no signing key of its own)
//	ProductService - what things actually cost, and is there stock
//
// Those two hops are what make service discovery, load balancing and
// distributed tracing demonstrable rather than theoretical. They are also why
// a trace of one order has three spans.
type Service struct {
	store    *Store
	users    userv1.UserServiceClient
	products productv1.ProductServiceClient
	log      *slog.Logger

	orderv1.UnimplementedOrderServiceServer
}

func NewService(
	store *Store,
	users userv1.UserServiceClient,
	products productv1.ProductServiceClient,
	log *slog.Logger,
) *Service {
	return &Service{store: store, users: users, products: products, log: log}
}

func (s *Service) CreateOrder(ctx context.Context, req *orderv1.CreateOrderRequest) (*orderv1.CreateOrderResponse, error) {
	// Hop 1: who is this?
	userID, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}

	items := req.GetItems()
	if len(items) == 0 {
		return nil, status.Error(codes.InvalidArgument, "items is required")
	}
	rawKey := strings.TrimSpace(req.GetIdempotencyKey())
	if rawKey == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}
	// Namespaced so two users cannot collide on the same client-chosen key.
	key := userID + ":" + rawKey

	// Fast path: this request was already answered.
	if existing, err := s.store.ByIdempotencyKey(ctx, key); err == nil {
		return &orderv1.CreateOrderResponse{
			Order:            existing.AsProto(),
			IdempotentReplay: true,
		}, nil
	} else if !errors.Is(err, ErrNotFound) {
		s.log.Error("idempotency lookup failed", "err", err)
		return nil, status.Error(codes.Internal, "could not place order")
	}

	// Hop 2: what does the catalogue say? The request carries no prices, so
	// this is the only source of them.
	cart := make([]*productv1.CartItem, 0, len(items))
	for _, it := range items {
		if strings.TrimSpace(it.GetProductId()) == "" {
			return nil, status.Error(codes.InvalidArgument, "product_id is required on every item")
		}
		if it.GetQuantity() <= 0 {
			return nil, status.Error(codes.InvalidArgument, "quantity must be greater than zero")
		}
		cart = append(cart, &productv1.CartItem{
			ProductId: it.GetProductId(),
			Quantity:  it.GetQuantity(),
		})
	}

	avail, err := s.products.CheckAvailability(ctx, &productv1.CheckAvailabilityRequest{Items: cart})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.InvalidArgument {
			return nil, status.Error(codes.InvalidArgument, st.Message())
		}
		s.log.Error("product service call failed", "err", err)
		return nil, status.Error(codes.Unavailable, "catalogue unavailable")
	}

	order := &Order{UserID: userID, IdempotencyKey: key, Currency: "INR"}

	if !avail.GetAllAvailable() {
		// A rejection is a business outcome, not a transport error: the caller
		// gets OK plus a REJECTED order it can explain to the customer.
		order.Status = int32(orderv1.OrderStatus_ORDER_STATUS_REJECTED)
		order.RejectionReason = int32(firstProblem(avail.GetResults()))
		if err := s.persist(ctx, order, key); err != nil {
			return nil, err
		}
		return &orderv1.CreateOrderResponse{Order: order.AsProto()}, nil
	}

	// Totals are computed from the catalogue's prices, never the client's.
	byID := make(map[string]*productv1.AvailabilityResult, len(avail.GetResults()))
	for _, r := range avail.GetResults() {
		byID[r.GetProductId()] = r
		if c := r.GetCurrency(); c != "" {
			order.Currency = c
		}
	}
	for _, it := range items {
		r := byID[it.GetProductId()]
		if r == nil {
			s.log.Error("availability result missing for item", "product_id", it.GetProductId())
			return nil, status.Error(codes.Internal, "could not place order")
		}
		lineTotal := r.GetUnitPriceMinor() * int64(it.GetQuantity())
		order.TotalMinor += lineTotal
		order.Lines = append(order.Lines, OrderLine{
			ProductID:      r.GetProductId(),
			Title:          r.GetTitle(),
			Quantity:       it.GetQuantity(),
			UnitPriceMinor: r.GetUnitPriceMinor(),
			LineTotalMinor: lineTotal,
		})
	}
	order.Status = int32(orderv1.OrderStatus_ORDER_STATUS_CONFIRMED)

	if err := s.persist(ctx, order, key); err != nil {
		return nil, err
	}
	return &orderv1.CreateOrderResponse{Order: order.AsProto()}, nil
}

// persist writes the order, falling back to the stored row if a concurrent
// retry won the race on the unique index.
func (s *Service) persist(ctx context.Context, o *Order, key string) error {
	err := s.store.Create(ctx, o)
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		if existing, lookupErr := s.store.ByIdempotencyKey(ctx, key); lookupErr == nil {
			*o = *existing
			return nil
		}
	}
	s.log.Error("persist order failed", "err", err)
	return status.Error(codes.Internal, "could not place order")
}

func (s *Service) GetOrder(ctx context.Context, req *orderv1.GetOrderRequest) (*orderv1.GetOrderResponse, error) {
	userID, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.GetId())
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	o, err := s.store.ByID(ctx, userID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "order not found")
		}
		s.log.Error("get order failed", "err", err)
		return nil, status.Error(codes.Internal, "could not fetch order")
	}
	return &orderv1.GetOrderResponse{Order: o.AsProto()}, nil
}

func (s *Service) ListOrders(ctx context.Context, req *orderv1.ListOrdersRequest) (*orderv1.ListOrdersResponse, error) {
	userID, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	size := int(req.GetPageSize())
	if size <= 0 {
		size = 20
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}
	offset, err := parsePageToken(req.GetPageToken())
	if err != nil {
		return nil, err
	}

	rows, err := s.store.List(ctx, userID, size+1, offset)
	if err != nil {
		s.log.Error("list orders failed", "err", err)
		return nil, status.Error(codes.Internal, "could not list orders")
	}
	var next string
	if len(rows) > size {
		rows = rows[:size]
		next = fmt.Sprintf("%d", offset+size)
	}
	out := make([]*orderv1.Order, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].AsProto())
	}
	return &orderv1.ListOrdersResponse{Orders: out, NextPageToken: next}, nil
}

// authenticate forwards the caller's bearer token to UserService. OrderService
// deliberately holds no signing key.
func (s *Service) authenticate(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	auth := md.Get("authorization")
	if len(auth) == 0 || strings.TrimSpace(auth[0]) == "" {
		return "", status.Error(codes.Unauthenticated, "missing authorization token")
	}
	// Propagate the credential outbound. Trace context rides along separately
	// via the OTel stats handler, which is what links the spans.
	outCtx := metadata.AppendToOutgoingContext(ctx, "authorization", auth[0])

	resp, err := s.users.VerifyToken(outCtx, &userv1.VerifyTokenRequest{})
	if err != nil {
		// An authentication failure passes through unchanged; anything else is
		// an upstream problem and must not look like a bad token.
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unauthenticated {
			return "", status.Error(codes.Unauthenticated, st.Message())
		}
		s.log.Error("user service call failed", "err", err)
		return "", status.Error(codes.Unavailable, "user service unavailable")
	}
	userID := resp.GetUser().GetId()
	if userID == "" {
		return "", status.Error(codes.Unauthenticated, "user service returned no user")
	}
	return userID, nil
}

// firstProblem picks the reason to report when several items fail. Not-found
// outranks stock problems: a wrong product id is a client bug, not a
// transient condition.
func firstProblem(results []*productv1.AvailabilityResult) orderv1.RejectionReason {
	reason := orderv1.RejectionReason_REJECTION_REASON_UNSPECIFIED
	for _, r := range results {
		switch r.GetAvailability() {
		case productv1.Availability_AVAILABILITY_NOT_FOUND:
			return orderv1.RejectionReason_REJECTION_REASON_PRODUCT_NOT_FOUND
		case productv1.Availability_AVAILABILITY_OUT_OF_STOCK:
			reason = orderv1.RejectionReason_REJECTION_REASON_OUT_OF_STOCK
		case productv1.Availability_AVAILABILITY_INSUFFICIENT_STOCK:
			if reason == orderv1.RejectionReason_REJECTION_REASON_UNSPECIFIED {
				reason = orderv1.RejectionReason_REJECTION_REASON_INSUFFICIENT_STOCK
			}
		}
	}
	return reason
}

func parsePageToken(token string) (int, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, nil
	}
	var offset int
	if _, err := fmt.Sscanf(token, "%d", &offset); err != nil || offset < 0 {
		return 0, status.Error(codes.InvalidArgument, "invalid page_token")
	}
	return offset, nil
}
