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

var memberTestColumns = []string{"id", "tenant_id", "user_id", "username", "display_name", "status", "joined_at", "created_at", "created_by", "updated_at", "updated_by", "version"}

func memberTestRow(id, status string, version int64) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows(memberTestColumns).AddRow(id, "tenant-a", "owner-1", "alice", "Alice", status, now, now, "admin", now, "admin", version)
}

func TestMembershipAddReturnsCommittedRowWhenActorPresentationFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenants WHERE id=\? AND status='active' AND deleted_at IS NULL`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`INSERT INTO tenant_memberships`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.id=\? AND tm.deleted_at IS NULL`).WithArgs("tenant-a", sqlmock.AnyArg()).WillReturnRows(memberTestRow("membership-a", "active", 1))
	mock.ExpectCommit()
	service := &MembershipService{repository: NewRepository(db), transactor: database.NewTransactor(db), users: mutationOwnerResolver{actorErr: errors.New("identity unavailable")}}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeSystem, TenantID: "tenant-a"})
	member, err := service.Add(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, "membership-a", member.ID)
	require.Equal(t, "admin", member.CreatedByName)
	require.Equal(t, int64(1), member.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMembershipUpdateStatusReturnsCommittedRowWhenActorPresentationFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery(`SELECT .* FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.id=\? AND tm.deleted_at IS NULL AND \(1 = 1\)`).WithArgs("tenant-a", "membership-a").WillReturnRows(memberTestRow("membership-a", "disabled", 2))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenants WHERE id=\? AND status='active' AND deleted_at IS NULL`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`UPDATE tenant_memberships AS tm SET status=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.id=\? AND tm.deleted_at IS NULL`).WithArgs("tenant-a", "membership-a").WillReturnRows(memberTestRow("membership-a", "active", 3))
	mock.ExpectCommit()
	service := &MembershipService{repository: NewRepository(db), transactor: database.NewTransactor(db), users: mutationOwnerResolver{actorErr: errors.New("identity unavailable")}}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeSystem, TenantID: "tenant-a"})
	member, err := service.UpdateStatus(ctx, "membership-a", StatusActive, 2)
	require.NoError(t, err)
	require.Equal(t, StatusActive, member.Status)
	require.Equal(t, "admin", member.UpdatedByName)
	require.Equal(t, int64(3), member.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMembershipAddRollsBackWhenResponseReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenants WHERE id=\? AND status='active' AND deleted_at IS NULL`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`INSERT INTO tenant_memberships`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.id=\? AND tm.deleted_at IS NULL`).WithArgs("tenant-a", sqlmock.AnyArg()).WillReturnError(errors.New("read unavailable"))
	mock.ExpectRollback()
	service := &MembershipService{repository: NewRepository(db), transactor: database.NewTransactor(db), users: mutationOwnerResolver{}}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeSystem, TenantID: "tenant-a"})
	_, err = service.Add(ctx, "alice")
	require.ErrorContains(t, err, "read unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMembershipUpdateStatusRollsBackWhenResponseReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery(`SELECT .* FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.id=\? AND tm.deleted_at IS NULL AND \(1 = 1\)`).WithArgs("tenant-a", "membership-a").WillReturnRows(memberTestRow("membership-a", "disabled", 2))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenants WHERE id=\? AND status='active' AND deleted_at IS NULL`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`UPDATE tenant_memberships AS tm SET status=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.id=\? AND tm.deleted_at IS NULL`).WithArgs("tenant-a", "membership-a").WillReturnError(errors.New("read unavailable"))
	mock.ExpectRollback()
	service := &MembershipService{repository: NewRepository(db), transactor: database.NewTransactor(db)}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeSystem, TenantID: "tenant-a"})
	_, err = service.UpdateStatus(ctx, "membership-a", StatusActive, 2)
	require.ErrorContains(t, err, "read unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}
