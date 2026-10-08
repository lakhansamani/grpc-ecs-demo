package payment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	identityv1 "github.com/lakhansamani/grpc-ecs-payments/gen/go/identity/v1"
	paymentv1 "github.com/lakhansamani/grpc-ecs-payments/gen/go/payment/v1"
	"github.com/lakhansamani/grpc-ecs-payments/internal/payment/explain"
)

// MaxPageSize caps ListTransactions.
const MaxPageSize = 100

// Service implements paymentv1.PaymentServiceServer.
//
// It never validates a JWT itself: it forwards the caller's authorization
// metadata to identityd over gRPC. That delegation is a real trust boundary
// and a real second network hop, which is what makes service discovery,
// load balancing and distributed tracing demonstrable rather than theoretical.
type Service struct {
	store    *Store
	identity identityv1.IdentityServiceClient
	rules    Rules
	explain  explain.Explainer
	log      *slog.Logger

	paymentv1.UnimplementedPaymentServiceServer
}

func NewService(
	store *Store,
	identity identityv1.IdentityServiceClient,
	rules Rules,
	explainer explain.Explainer,
	log *slog.Logger,
) *Service {
	return &Service{store: store, identity: identity, rules: rules, explain: explainer, log: log}
}

func (s *Service) Authorize(ctx context.Context, req *paymentv1.AuthorizeRequest) (*paymentv1.AuthorizeResponse, error) {
	userID, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}

	amount := req.GetAmountMinor()
	if amount <= 0 {
		return nil, status.Error(codes.InvalidArgument, "amount_minor must be greater than zero")
	}
	currency := strings.ToUpper(strings.TrimSpace(req.GetCurrency()))
	if currency == "" {
		currency = "INR"
	}
	if len(currency) != 3 {
		return nil, status.Error(codes.InvalidArgument, "currency must be a 3-letter ISO-4217 code")
	}
	rawKey := strings.TrimSpace(req.GetIdempotencyKey())
	if rawKey == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}
	// Namespaced so two users cannot collide on the same client-chosen key.
	key := userID + ":" + rawKey

	// Fast path: this request was already answered.
	if existing, err := s.store.ByIdempotencyKey(ctx, key); err == nil {
		return &paymentv1.AuthorizeResponse{
			Transaction:      existing.AsProto(),
			IdempotentReplay: true,
		}, nil
	} else if !errors.Is(err, ErrNotFound) {
		s.log.Error("idempotency lookup failed", "err", err)
		return nil, status.Error(codes.Internal, "could not authorize")
	}

	since := time.Now().Add(-s.rules.VelocityWindow).UnixMilli()
	attempts, spent, err := s.store.Stats(ctx, userID, since)
	if err != nil {
		s.log.Error("stats failed", "err", err)
		return nil, status.Error(codes.Internal, "could not authorize")
	}

	outcome := s.rules.Evaluate(Input{
		AmountMinor:      amount,
		Currency:         currency,
		MerchantID:       strings.TrimSpace(req.GetMerchantId()),
		MerchantCategory: strings.TrimSpace(req.GetMerchantCategory()),
		AttemptsInWindow: attempts,
		SpentMinor:       spent,
	})

	txn := &Transaction{
		UserID:           userID,
		AmountMinor:      amount,
		Currency:         currency,
		MerchantID:       strings.TrimSpace(req.GetMerchantId()),
		MerchantCategory: strings.TrimSpace(req.GetMerchantCategory()),
		Decision:         decisionToProto(outcome.Decision),
		DeclineReason:    reasonToProto(outcome.Reason),
		IdempotencyKey:   key,
	}
	if err := s.store.Create(ctx, txn); err != nil {
		// Lost a race with a concurrent retry: the unique index is the
		// authority, so return whatever it stored.
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			existing, lookupErr := s.store.ByIdempotencyKey(ctx, key)
			if lookupErr == nil {
				return &paymentv1.AuthorizeResponse{
					Transaction:      existing.AsProto(),
					IdempotentReplay: true,
				}, nil
			}
		}
		s.log.Error("persist transaction failed", "err", err)
		return nil, status.Error(codes.Internal, "could not authorize")
	}

	// Declines are a normal business outcome, not a transport error: the
	// caller gets OK plus a DECLINED decision, so it can show the reason
	// instead of guessing from an error string.
	return &paymentv1.AuthorizeResponse{Transaction: txn.AsProto()}, nil
}

func (s *Service) GetTransaction(ctx context.Context, req *paymentv1.GetTransactionRequest) (*paymentv1.GetTransactionResponse, error) {
	userID, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.GetId())
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	txn, err := s.store.ByID(ctx, userID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "transaction not found")
		}
		s.log.Error("get transaction failed", "err", err)
		return nil, status.Error(codes.Internal, "could not fetch transaction")
	}
	return &paymentv1.GetTransactionResponse{Transaction: txn.AsProto()}, nil
}

func (s *Service) ListTransactions(ctx context.Context, req *paymentv1.ListTransactionsRequest) (*paymentv1.ListTransactionsResponse, error) {
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

	// Fetch one extra to decide whether a next page exists.
	rows, err := s.store.List(ctx, userID, size+1, offset)
	if err != nil {
		s.log.Error("list transactions failed", "err", err)
		return nil, status.Error(codes.Internal, "could not list transactions")
	}

	var next string
	if len(rows) > size {
		rows = rows[:size]
		next = fmt.Sprintf("%d", offset+size)
	}
	out := make([]*paymentv1.Transaction, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].AsProto())
	}
	return &paymentv1.ListTransactionsResponse{Transactions: out, NextPageToken: next}, nil
}

func (s *Service) ExplainDecision(ctx context.Context, req *paymentv1.ExplainDecisionRequest) (*paymentv1.ExplainDecisionResponse, error) {
	userID, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.GetTransactionId())
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "transaction_id is required")
	}
	txn, err := s.store.ByID(ctx, userID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "transaction not found")
		}
		s.log.Error("explain lookup failed", "err", err)
		return nil, status.Error(codes.Internal, "could not explain decision")
	}

	// Rebuild the facts from what was stored, so the explanation describes the
	// decision that was actually made rather than re-running today's rules.
	since := txn.CreatedAt - s.rules.VelocityWindow.Milliseconds()
	attempts, spent, err := s.store.Stats(ctx, userID, since)
	if err != nil {
		attempts, spent = 0, 0 // an explanation is a nicety; never fail for it
	}
	balance := s.rules.OpeningBalanceMinor - spent
	if balance < 0 {
		balance = 0
	}

	facts := explain.Facts{
		Decision:         explain.Decision(boolToDecision(txn.Decision)),
		Reason:           reasonFromProto(paymentv1.DeclineReason(txn.DeclineReason)),
		AmountMinor:      txn.AmountMinor,
		Currency:         txn.Currency,
		PerTxnLimitMinor: s.rules.PerTxnLimitMinor,
		BalanceMinor:     balance,
		AttemptsInWindow: attempts,
		VelocityLimit:    s.rules.VelocityLimit,
		WindowMinutes:    int(s.rules.VelocityWindow / time.Minute),
		MerchantID:       txn.MerchantID,
		MerchantCategory: txn.MerchantCategory,
	}

	text, provider, err := s.explain.Explain(ctx, facts)
	if err != nil {
		s.log.Error("explain failed", "err", err)
		return nil, status.Error(codes.Internal, "could not explain decision")
	}
	return &paymentv1.ExplainDecisionResponse{Explanation: text, Provider: provider}, nil
}

// authenticate forwards the caller's bearer token to identityd and returns the
// resolved user id. paymentd deliberately holds no signing key.
func (s *Service) authenticate(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	auth := md.Get("authorization")
	if len(auth) == 0 || strings.TrimSpace(auth[0]) == "" {
		return "", status.Error(codes.Unauthenticated, "missing authorization token")
	}

	// Propagate the credential on the outgoing call. The OTel stats handler
	// adds trace context separately, which is what links the two spans.
	outCtx := metadata.AppendToOutgoingContext(ctx, "authorization", auth[0])

	resp, err := s.identity.VerifyToken(outCtx, &identityv1.VerifyTokenRequest{})
	if err != nil {
		// Pass an authentication failure through unchanged; anything else is
		// an upstream dependency problem and must not look like a bad token.
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unauthenticated {
			return "", status.Error(codes.Unauthenticated, st.Message())
		}
		s.log.Error("identity service call failed", "err", err)
		return "", status.Error(codes.Unavailable, "identity service unavailable")
	}
	userID := resp.GetUser().GetId()
	if userID == "" {
		return "", status.Error(codes.Unauthenticated, "identity service returned no user")
	}
	return userID, nil
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

func boolToDecision(stored int32) int {
	if stored == int32(paymentv1.Decision_DECISION_APPROVED) {
		return int(explain.Approved)
	}
	return int(explain.Declined)
}
