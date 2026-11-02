package tenant

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestMembershipAddRestoresDeletedMembershipWithSameIdentityAndNewVersion(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT id,version,created_by FROM tenant_memberships WHERE tenant_id=\? AND user_id=\? AND deleted_at IS NOT NULL`).
		WithArgs("tenant-a", "owner-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "version", "created_by"}).AddRow("membership-old", 2, "original-admin"))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenants WHERE id=\? AND status='active' AND deleted_at IS NULL`).
		WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`UPDATE tenant_memberships SET username=\?,display_name=\?,status=\?,joined_at=\?,deleted_at=NULL,deleted_by=NULL,updated_at=\?,updated_by=\?,version=version\+1 WHERE tenant_id=\? AND id=\? AND user_id=\? AND version=\? AND deleted_at IS NOT NULL`).
		WithArgs("alice", "Owner One", StatusActive, sqlmock.AnyArg(), sqlmock.AnyArg(), "admin", "tenant-a", "membership-old", "owner-1", int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.id=\? AND tm.deleted_at IS NULL`).
		WithArgs("tenant-a", "membership-old").
		WillReturnRows(sqlmock.NewRows(memberTestColumns).AddRow("membership-old", "tenant-a", "owner-1", "alice", "Owner One", StatusActive, now, now.Add(-time.Hour), "original-admin", now, "admin", 3))
	mock.ExpectCommit()
	operations := &transactionalOperationRecorderStub{}
	security := &transactionalSecurityRecorderStub{}
	service := &MembershipService{repository: NewRepository(db), transactor: database.NewTransactor(db), users: mutationOwnerResolver{}, operations: operations, security: security}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeSystem, TenantID: "tenant-a"})
	member, err := service.Add(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, "membership-old", member.ID)
	require.Equal(t, int64(3), member.Version)
	require.Equal(t, "original-admin", member.CreatedBy)
	require.Equal(t, "admin", member.UpdatedBy)
	require.Len(t, operations.txEntries, 1)
	require.Equal(t, "membership-old", operations.txEntries[0].ResourceID)
	require.Len(t, security.txEntries, 1)
	require.Equal(t, "owner-1", security.txEntries[0].SubjectID)
	_, offset := member.JoinedAt.Zone()
	require.Equal(t, 8*60*60, offset)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMembershipAddRejectsStaleDeletedMembershipRestore(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery(`SELECT id,version,created_by FROM tenant_memberships WHERE tenant_id=\? AND user_id=\? AND deleted_at IS NOT NULL`).
		WithArgs("tenant-a", "owner-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "version", "created_by"}).AddRow("membership-old", 2, "original-admin"))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenants WHERE id=\? AND status='active' AND deleted_at IS NULL`).
		WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`UPDATE tenant_memberships SET username=\?`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	service := &MembershipService{repository: NewRepository(db), transactor: database.NewTransactor(db), users: mutationOwnerResolver{}}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeSystem, TenantID: "tenant-a"})
	_, err = service.Add(ctx, "alice")
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMembershipAddFailsClosedWhenDeletedMembershipLookupFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	wantErr := errors.New("membership lookup unavailable")
	mock.ExpectQuery(`SELECT id,version,created_by FROM tenant_memberships WHERE tenant_id=\? AND user_id=\? AND deleted_at IS NOT NULL`).
		WithArgs("tenant-a", "owner-1").WillReturnError(wantErr)
	service := &MembershipService{repository: NewRepository(db), users: mutationOwnerResolver{}}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeSystem, TenantID: "tenant-a"})
	_, err = service.Add(ctx, "alice")
	require.ErrorIs(t, err, wantErr)
	require.NoError(t, mock.ExpectationsWereMet())
}
