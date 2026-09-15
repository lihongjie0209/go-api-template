package grpctransport

import (
	"context"
	"errors"

	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	identityv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/identity/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type identityServer struct {
	identityv1.UnimplementedIdentityServiceServer
	service *identity.Service
}

func (s *identityServer) GetUser(ctx context.Context, r *identityv1.GetUserRequest) (*identityv1.GetUserResponse, error) {
	u, e := s.service.Get(ctx, r.GetUserId())
	if e != nil {
		return nil, identityError(e)
	}
	return &identityv1.GetUserResponse{User: toProtoUser(u)}, nil
}
func (s *identityServer) BatchGetUsers(ctx context.Context, r *identityv1.BatchGetUsersRequest) (*identityv1.BatchGetUsersResponse, error) {
	if len(r.GetUserIds()) > 200 {
		return nil, status.Error(codes.InvalidArgument, "at most 200 user ids")
	}
	result, e := s.service.Page(ctx, identity.PageInput{Request: pagination.Request{Page: 1, PageSize: 200}, IDs: r.GetUserIds()})
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
