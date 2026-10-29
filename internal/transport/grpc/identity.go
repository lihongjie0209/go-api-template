package grpctransport

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/serviceaccount"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	identityv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/identity/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type identityServer struct {
	identityv1.UnimplementedIdentityServiceServer
	service     *identity.Service
	auth        *auth.Service
	accounts    *serviceaccount.Service
	jwtAudience string
}

func (s *identityServer) GetServiceAccount(ctx context.Context, r *identityv1.GetServiceAccountRequest) (*identityv1.GetServiceAccountResponse, error) {
	principal, err := platformprincipal.Require(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}
	if principal.Type != platformprincipal.TypeServiceAccount {
		return nil, status.Error(codes.PermissionDenied, "service authentication required")
	}
	if s.accounts == nil {
		return nil, status.Error(codes.Unavailable, "service account state unavailable")
	}
	account, err := s.accounts.Get(ctx, r.GetServiceAccountId())
	if errors.Is(err, serviceaccount.ErrInvalid) {
		return nil, status.Error(codes.InvalidArgument, "invalid service account id")
	}
	if errors.Is(err, serviceaccount.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "service account not found")
	}
	if errors.Is(err, context.Canceled) {
		return nil, status.Error(codes.Canceled, "request canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, status.Error(codes.DeadlineExceeded, "request deadline exceeded")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "service account state unavailable")
	}
	now := time.Now()
	// A temporary secret-login lock does not invalidate previously issued
	// bearer tokens; the authoritative bearer verifier checks status/expiry.
	enabled := account.Status == serviceaccount.StatusActive && (account.ExpiresAt == nil || account.ExpiresAt.After(now))
	response := &identityv1.GetServiceAccountResponse{Id: account.ID, Name: account.Name, Enabled: enabled}
	if s.jwtAudience != "" {
		response.Audiences = []string{s.jwtAudience}
	}
	return response, nil
}

func (s *identityServer) ValidateSession(ctx context.Context, r *identityv1.ValidateSessionRequest) (*identityv1.ValidateSessionResponse, error) {
	principal, err := platformprincipal.Require(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}
	if principal.Type != platformprincipal.TypeServiceAccount {
		return nil, status.Error(codes.PermissionDenied, "service authentication required")
	}
	if s.auth == nil {
		return nil, status.Error(codes.Unavailable, "session state unavailable")
	}
	expiresAt, valid, err := s.auth.ValidateSession(ctx, r.GetSessionId(), r.GetUserId())
	if errors.Is(err, auth.ErrInvalidSessionLookup) {
		return nil, status.Error(codes.InvalidArgument, "session id and user id are required")
	}
	if errors.Is(err, context.Canceled) {
		return nil, status.Error(codes.Canceled, "request canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, status.Error(codes.DeadlineExceeded, "request deadline exceeded")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "session state unavailable")
	}
	response := &identityv1.ValidateSessionResponse{Valid: valid}
	if valid {
		response.ExpiresAt = timestamppb.New(expiresAt)
	}
	return response, nil
}

func (s *identityServer) GetUser(ctx context.Context, r *identityv1.GetUserRequest) (*identityv1.GetUserResponse, error) {
	u, e := s.service.Get(ctx, r.GetUserId())
	if e != nil {
		return nil, identityError(e)
	}
	return &identityv1.GetUserResponse{User: toProtoUser(u)}, nil
}
func (s *identityServer) BatchGetUsers(ctx context.Context, r *identityv1.BatchGetUsersRequest) (*identityv1.BatchGetUsersResponse, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}
	ids := r.GetUserIds()
	if len(ids) > 200 {
		return nil, status.Error(codes.InvalidArgument, "at most 200 user ids")
	}
	if len(ids) == 0 {
		// An empty IN set must not turn into an unfiltered user page.
		return &identityv1.BatchGetUsersResponse{Users: []*identityv1.User{}}, nil
	}
	result, e := s.service.Page(ctx, identity.PageInput{Request: pagination.Request{Page: 1, PageSize: 200}, IDs: ids})
	if e != nil {
		return nil, identityError(e)
	}
	users := make([]*identityv1.User, len(result.Items))
	for i := range result.Items {
		users[i] = toProtoUser(result.Items[i])
	}
	return &identityv1.BatchGetUsersResponse{Users: users}, nil
}
func (s *identityServer) ListUsers(ctx context.Context, r *identityv1.ListUsersRequest) (*identityv1.ListUsersResponse, error) {
	if username, exact := strings.CutPrefix(strings.TrimSpace(r.GetKeyword()), "="); exact {
		// Exact user resolution can gate membership creation. Never decide active
		// eligibility from a cache that may survive failed invalidation.
		u, e := s.service.ResolveUsernameAuthoritative(ctx, username)
		if errors.Is(e, identity.ErrNotFound) || e == nil && r.GetStatus() != identityv1.UserStatus_USER_STATUS_UNSPECIFIED && toProtoStatus(u.Status) != r.GetStatus() {
			return &identityv1.ListUsersResponse{Users: []*identityv1.User{}, Page: &commonv1.PageResult{Page: 1, PageSize: 1, Total: 0}}, nil
		}
		if e != nil {
			return nil, identityError(e)
		}
		return &identityv1.ListUsersResponse{Users: []*identityv1.User{toProtoUser(u)}, Page: &commonv1.PageResult{Page: 1, PageSize: 1, Total: 1}}, nil
	}
	page, pageSize := 1, 20
	if r.GetPage() != nil {
		page = int(r.GetPage().GetPage())
		pageSize = int(r.GetPage().GetPageSize())
	}
	input := identity.PageInput{Request: pagination.Request{Page: page, PageSize: pageSize, Keyword: r.GetKeyword()}}
	if r.GetStatus() != identityv1.UserStatus_USER_STATUS_UNSPECIFIED {
		input.Statuses = []identity.Status{fromProtoStatus(r.GetStatus())}
	}
	result, e := s.service.Page(ctx, input)
	if e != nil {
		return nil, identityError(e)
	}
	users := make([]*identityv1.User, len(result.Items))
	for i := range result.Items {
		users[i] = toProtoUser(result.Items[i])
	}
	return &identityv1.ListUsersResponse{Users: users, Page: &commonv1.PageResult{Page: uint32(result.Page), PageSize: uint32(result.PageSize), Total: uint64(result.Total)}}, nil
}
func toProtoUser(u identity.User) *identityv1.User {
	return &identityv1.User{Id: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: u.Email, Phone: u.Phone, Status: toProtoStatus(u.Status), CreatedAt: timestamppb.New(u.CreatedAt), UpdatedAt: timestamppb.New(u.UpdatedAt), Version: u.Version, CreatedBy: u.CreatedBy, UpdatedBy: u.UpdatedBy}
}
func toProtoStatus(v identity.Status) identityv1.UserStatus {
	switch v {
	case identity.StatusActive:
		return identityv1.UserStatus_USER_STATUS_ACTIVE
	case identity.StatusDisabled:
		return identityv1.UserStatus_USER_STATUS_DISABLED
	case identity.StatusLocked:
		return identityv1.UserStatus_USER_STATUS_LOCKED
	case identity.StatusClosed:
		return identityv1.UserStatus_USER_STATUS_CLOSED
	}
	return identityv1.UserStatus_USER_STATUS_UNSPECIFIED
}
func fromProtoStatus(v identityv1.UserStatus) identity.Status {
	switch v {
	case identityv1.UserStatus_USER_STATUS_ACTIVE:
		return identity.StatusActive
	case identityv1.UserStatus_USER_STATUS_DISABLED:
		return identity.StatusDisabled
	case identityv1.UserStatus_USER_STATUS_LOCKED:
		return identity.StatusLocked
	case identityv1.UserStatus_USER_STATUS_CLOSED:
		return identity.StatusClosed
	}
	return ""
}
func identityError(e error) error {
	switch {
	case errors.Is(e, identity.ErrInvalid):
		return status.Error(codes.InvalidArgument, "invalid user request")
	case errors.Is(e, identity.ErrNotFound):
		return status.Error(codes.NotFound, "user not found")
	case errors.Is(e, identity.ErrConflict):
		return status.Error(codes.Aborted, "user version conflict")
	default:
		return status.Error(codes.Internal, "identity operation failed")
	}
}
