package tenant

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
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

func TestMembershipAddDoesNotReportReleaseFailureAfterCommit(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenants WHERE id=\? AND status='active' AND deleted_at IS NULL`).
		WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`INSERT INTO tenant_memberships`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.id=\? AND tm.deleted_at IS NULL`).
		WithArgs("tenant-a", sqlmock.AnyArg()).WillReturnRows(memberTestRow("membership-a", "active", 1))
	mock.ExpectCommit()
	service := &MembershipService{
		repository: NewRepository(db), transactor: database.NewTransactor(db),
		users:  mutationOwnerResolver{actorErr: errors.New("identity unavailable")},
		locker: testutil.LockStub{ReleaseErr: cache.ErrLockLost},
		cfg:    config.Config{DistributedLock: config.DistributedLock{TTL: time.Second, RetryDelay: time.Millisecond}},
	}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeSystem, TenantID: "tenant-a"})
	member, err := service.Add(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, "membership-a", member.ID)
	require.Equal(t, int64(1), member.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}
