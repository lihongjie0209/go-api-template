package tenant

import (
	"context"
	"testing"
	"time"

	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	identityv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/identity/v1"
	"google.golang.org/grpc"
)

type identityClientStub struct {
	identityv1.IdentityServiceClient
	request      *identityv1.ListUsersRequest
	batchRequest *identityv1.BatchGetUsersRequest
}

func TestMembershipPresentationUsesIdentityBatchAndPlatformTimezone(t *testing.T) {
	t.Parallel()
	client := &identityClientStub{}
	service := &MembershipService{users: &grpcUserResolver{client: client}}
	instant := time.Date(2026, time.September, 16, 1, 2, 3, 0, time.UTC)
	members := []Member{{CreatedBy: "user-1", UpdatedBy: "system-1", JoinedAt: instant, CreatedAt: instant, UpdatedAt: instant}}
	if err := service.presentMembers(t.Context(), members); err != nil {
		t.Fatal(err)
	}
	if members[0].CreatedByName != "Alice" || members[0].UpdatedByName != "system-1" || members[0].JoinedAt.Format(time.RFC3339) != "2026-09-16T09:02:03+08:00" {
		t.Fatalf("member = %+v", members[0])
	}
}

func (s *identityClientStub) BatchGetUsers(_ context.Context, request *identityv1.BatchGetUsersRequest, _ ...grpc.CallOption) (*identityv1.BatchGetUsersResponse, error) {
	s.batchRequest = request
	return &identityv1.BatchGetUsersResponse{Users: []*identityv1.User{{Id: "user-1", DisplayName: "Alice"}}}, nil
}

func (s *identityClientStub) ListUsers(_ context.Context, request *identityv1.ListUsersRequest, _ ...grpc.CallOption) (*identityv1.ListUsersResponse, error) {
	s.request = request
	return &identityv1.ListUsersResponse{Users: []*identityv1.User{{Id: "user-1", Username: "alice", DisplayName: "Alice", Status: identityv1.UserStatus_USER_STATUS_ACTIVE}}, Page: &commonv1.PageResult{Page: 1, PageSize: 1, Total: 1}}, nil
}

func TestGRPCUserResolverBatchResolvesOnceWithStableFallback(t *testing.T) {
	t.Parallel()
	client := &identityClientStub{}
	resolver := &grpcUserResolver{client: client}
	names, err := resolver.ResolveUserIDs(t.Context(), []string{"user-1", "system-1", "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if names["user-1"] != "Alice" || names["system-1"] != "system-1" || len(client.batchRequest.GetUserIds()) != 2 {
		t.Fatalf("names=%#v request=%+v", names, client.batchRequest)
	}
}

func TestGRPCUserResolverUsesSingleExactActiveLookup(t *testing.T) {
	t.Parallel()
	client := &identityClientStub{}
	resolver := &grpcUserResolver{client: client}
	user, err := resolver.ResolveUsername(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != "user-1" || client.request == nil || client.request.GetKeyword() != "=alice" || client.request.GetStatus() != identityv1.UserStatus_USER_STATUS_ACTIVE || client.request.GetPage().GetPage() != 1 || client.request.GetPage().GetPageSize() != 1 {
		t.Fatalf("user=%+v request=%+v", user, client.request)
	}
}
