// Package identity implements UserService: users, passwords and tokens.
package user

import (
	"context"
	"errors"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	userv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/user/v1"
)

// MinPasswordLen is the shortest password accepted at registration.
const MinPasswordLen = 8

// Service implements userv1.UserServiceServer.
//
// Every error returned here carries a real gRPC status code. The version this
// replaces used errors.New everywhere, so a wrong password, a missing user and
// a panicking database were all codes.Unknown to the caller (defect #2) - which
// makes retry logic and client-side error handling impossible to write.
type Service struct {
	store  *Store
	issuer *Issuer
	log    *slog.Logger

	userv1.UnimplementedUserServiceServer
}

func NewService(store *Store, issuer *Issuer, log *slog.Logger) *Service {
	return &Service{store: store, issuer: issuer, log: log}
}

func (s *Service) Register(ctx context.Context, req *userv1.RegisterRequest) (*userv1.RegisterResponse, error) {
	name := strings.TrimSpace(req.GetName())
	email := normalizeEmail(req.GetEmail())
	password := req.GetPassword()

	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if email == "" {
		return nil, status.Error(codes.InvalidArgument, "email is required")
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, status.Error(codes.InvalidArgument, "email is not a valid address")
	}
	if len(password) < MinPasswordLen {
		return nil, status.Errorf(codes.InvalidArgument,
			"password must be at least %d characters", MinPasswordLen)
	}

	u, err := s.store.Create(ctx, name, email, password)
	if err != nil {
		if errors.Is(err, ErrEmailTaken) {
			return nil, status.Error(codes.AlreadyExists, "email already registered")
		}
		s.log.Error("register failed", "err", err)
		return nil, status.Error(codes.Internal, "could not register")
	}
	return &userv1.RegisterResponse{UserId: u.ID}, nil
}

func (s *Service) Login(ctx context.Context, req *userv1.LoginRequest) (*userv1.LoginResponse, error) {
	email := normalizeEmail(req.GetEmail())
	password := req.GetPassword()

	if email == "" || password == "" {
		return nil, status.Error(codes.InvalidArgument, "email and password are required")
	}

	u, err := s.store.ByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Same message and code as a wrong password, deliberately: telling
			// a caller which emails exist is an account-enumeration oracle.
			return nil, status.Error(codes.Unauthenticated, "invalid email or password")
		}
		s.log.Error("login lookup failed", "err", err)
		return nil, status.Error(codes.Internal, "could not log in")
	}
	if !u.CheckPassword(password) {
		return nil, status.Error(codes.Unauthenticated, "invalid email or password")
	}

	token, expiresAt, err := s.issuer.Issue(u.ID, time.Now())
	if err != nil {
		s.log.Error("token issue failed", "err", err)
		return nil, status.Error(codes.Internal, "could not log in")
	}
	return &userv1.LoginResponse{Token: token, ExpiresAt: expiresAt}, nil
}

func (s *Service) VerifyToken(ctx context.Context, _ *userv1.VerifyTokenRequest) (*userv1.VerifyTokenResponse, error) {
	token, err := BearerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	userID, err := s.issuer.Verify(token)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired token")
	}
	u, err := s.store.ByID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Valid signature, but the subject is gone.
			return nil, status.Error(codes.Unauthenticated, "user no longer exists")
		}
		s.log.Error("verify lookup failed", "err", err)
		return nil, status.Error(codes.Internal, "could not verify token")
	}
	return &userv1.VerifyTokenResponse{
		User: &userv1.User{Id: u.ID, Name: u.Name, Email: u.Email},
	}, nil
}

// BearerFromContext pulls a bearer token out of gRPC metadata.
// Exported because paymentd forwards the same header and needs identical parsing.
func BearerFromContext(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	values := md.Get("authorization")
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return "", status.Error(codes.Unauthenticated, "missing authorization token")
	}
	scheme, token, found := strings.Cut(strings.TrimSpace(values[0]), " ")
	if !found || !strings.EqualFold(scheme, "bearer") || strings.TrimSpace(token) == "" {
		return "", status.Error(codes.Unauthenticated, `authorization must be "Bearer <token>"`)
	}
	return strings.TrimSpace(token), nil
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }
