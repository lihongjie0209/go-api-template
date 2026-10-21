package tenant

import (
	"context"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/testutil"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type memberLockUserResolver struct{}

func (memberLockUserResolver) ResolveUsername(context.Context, string) (UserSnapshot, error) {
	return UserSnapshot{ID: "user-1", Username: "alice", DisplayName: "Alice"}, nil
}

func TestMembershipAddPreservesLockAcquisitionFailure(t *testing.T) {
	t.Parallel()
	service := &MembershipService{
		users:  memberLockUserResolver{},
		locker: testutil.LockStub{AcquireErr: context.DeadlineExceeded},
		cfg:    config.Config{DistributedLock: config.DistributedLock{TTL: time.Second, RetryDelay: time.Millisecond}},
	}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1"})
	_, err := service.Add(ctx, "alice")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, ErrConflict)
}
