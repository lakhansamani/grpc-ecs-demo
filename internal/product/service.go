package product

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	productv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/product/v1"
)

// MaxPageSize caps list and search results.
const MaxPageSize = 50

// Service implements productv1.ProductServiceServer.
type Service struct {
	store *Store
	log   *slog.Logger

	productv1.UnimplementedProductServiceServer
}

func NewService(store *Store, log *slog.Logger) *Service {
	return &Service{store: store, log: log}
}

func (s *Service) ListProducts(ctx context.Context, req *productv1.ListProductsRequest) (*productv1.ListProductsResponse, error) {
	size := pageSize(req.GetPageSize())
	offset, err := parsePageToken(req.GetPageToken())
	if err != nil {
		return nil, err
	}

	// One extra row tells us whether a next page exists.
	rows, err := s.store.List(ctx, req.GetCategory(), size+1, offset)
	if err != nil {
		s.log.Error("list products failed", "err", err)
		return nil, status.Error(codes.Internal, "could not list products")
	}

	var next string
	if len(rows) > size {
		rows = rows[:size]
		next = fmt.Sprintf("%d", offset+size)
	}
	return &productv1.ListProductsResponse{
		Products:      toProtos(rows),
		NextPageToken: next,
	}, nil
}

func (s *Service) GetProduct(ctx context.Context, req *productv1.GetProductRequest) (*productv1.GetProductResponse, error) {
	id := strings.TrimSpace(req.GetId())
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	p, err := s.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "product not found")
		}
		s.log.Error("get product failed", "err", err)
		return nil, status.Error(codes.Internal, "could not fetch product")
	}
	return &productv1.GetProductResponse{Product: p.AsProto()}, nil
}

func (s *Service) SearchProducts(ctx context.Context, req *productv1.SearchProductsRequest) (*productv1.SearchProductsResponse, error) {
	query := strings.TrimSpace(req.GetQuery())
	if query == "" {
		return nil, status.Error(codes.InvalidArgument, "query is required")
	}
	rows, total, err := s.store.Search(ctx, query, pageSize(req.GetPageSize()))
	if err != nil {
		s.log.Error("search failed", "err", err, "query", query)
		return nil, status.Error(codes.Internal, "could not search")
	}
	// No matches is a valid answer, not an error.
	return &productv1.SearchProductsResponse{
		Products:     toProtos(rows),
		TotalMatches: int32(total),
	}, nil
}

// CheckAvailability is the internal-only RPC OrderService calls. It has no
// google.api.http annotation, so the REST gateway never exposes it - a client
// cannot discover the authoritative price here and then submit a different one.
func (s *Service) CheckAvailability(ctx context.Context, req *productv1.CheckAvailabilityRequest) (*productv1.CheckAvailabilityResponse, error) {
	items := req.GetItems()
	if len(items) == 0 {
		return nil, status.Error(codes.InvalidArgument, "items is required")
	}
	if len(items) > MaxPageSize {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d items per request", MaxPageSize)
	}

	ids := make([]string, 0, len(items))
	for _, it := range items {
		id := strings.TrimSpace(it.GetProductId())
		if id == "" {
			return nil, status.Error(codes.InvalidArgument, "product_id is required on every item")
		}
		if it.GetQuantity() <= 0 {
			return nil, status.Error(codes.InvalidArgument, "quantity must be greater than zero")
		}
		ids = append(ids, id)
	}

	found, err := s.store.ByIDs(ctx, ids)
	if err != nil {
		s.log.Error("availability lookup failed", "err", err)
		return nil, status.Error(codes.Internal, "could not check availability")
	}

	all := true
	results := make([]*productv1.AvailabilityResult, 0, len(items))
	for _, it := range items {
		id := strings.TrimSpace(it.GetProductId())
		p, ok := found[id]
		if !ok {
			all = false
			results = append(results, &productv1.AvailabilityResult{
				ProductId:    id,
				Availability: productv1.Availability_AVAILABILITY_NOT_FOUND,
			})
			continue
		}

		verdict := productv1.Availability_AVAILABILITY_OK
		switch {
		case p.Stock <= 0:
			verdict = productv1.Availability_AVAILABILITY_OUT_OF_STOCK
		case p.Stock < it.GetQuantity():
			verdict = productv1.Availability_AVAILABILITY_INSUFFICIENT_STOCK
		}
		if verdict != productv1.Availability_AVAILABILITY_OK {
			all = false
		}

		results = append(results, &productv1.AvailabilityResult{
			ProductId:      p.ID,
			Availability:   verdict,
			UnitPriceMinor: p.PriceMinor,
			Currency:       p.Currency,
			StockRemaining: p.Stock,
			Title:          p.Title,
		})
	}

	return &productv1.CheckAvailabilityResponse{Results: results, AllAvailable: all}, nil
}

func pageSize(requested int32) int {
	if requested <= 0 {
		return 20
	}
	if int(requested) > MaxPageSize {
		return MaxPageSize
	}
	return int(requested)
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

func toProtos(rows []Product) []*productv1.Product {
	out := make([]*productv1.Product, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].AsProto())
	}
	return out
}
