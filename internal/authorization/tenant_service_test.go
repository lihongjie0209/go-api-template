package authorization

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
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestNormalizeIDs(t *testing.T) {
	actual, err := normalizeIDs([]string{"b", "a", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, actual)
}

func TestEffectivePermissionIDsForAdministrator(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT g.permission_id FROM tenant_permission_grants`).WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("write").AddRow("read"))

	permissions, err := effectivePermissionIDs(context.Background(), sqlxDB, "tenant-1", "member-1")
	require.NoError(t, err)
	require.Equal(t, []string{"read", "write"}, permissions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEffectivePermissionIDsForRoleMemberFiltersByTenant(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id FROM tenant_role_permissions`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("read"))

	permissions, err := effectivePermissionIDs(context.Background(), sqlxDB, "tenant-1", "member-1")
	require.NoError(t, err)
	require.Equal(t, []string{"read"}, permissions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEffectivePermissionIDsRejectsInactiveMembershipBeforeGrants(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	_, err = effectivePermissionIDs(t.Context(), sqlxDB, "tenant-1", "member-1")
	require.ErrorIs(t, err, ErrTenantAuthorizationForbidden)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRequireSubset(t *testing.T) {
	require.NoError(t, requireSubset([]string{"read"}, []string{"write", "read"}))
	require.ErrorIs(t, requireSubset([]string{"delete"}, []string{"read"}), ErrTenantAuthorizationForbidden)
	require.True(t, errors.Is(requireSubset([]string{"delete"}, nil), ErrTenantAuthorizationForbidden))
}

func TestPruneRolePermissionsBumpsAffectedRoleVersions(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	mock.ExpectBegin()
	tx, err := sqlxDB.Beginx()
	require.NoError(t, err)
	mock.ExpectQuery(`SELECT DISTINCT role_id FROM tenant_role_permissions`).
		WithArgs("tenant-1", "permission-keep").
		WillReturnRows(sqlmock.NewRows([]string{"role_id"}).AddRow("role-1"))
	mock.ExpectExec(`UPDATE tenant_role_permissions SET deleted_at=`).
		WithArgs(sqlmock.AnyArg(), "actor-1", sqlmock.AnyArg(), "actor-1", "tenant-1", "permission-keep").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE tenant_roles SET updated_at=`).
		WithArgs(sqlmock.AnyArg(), "actor-1", "tenant-1", "role-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, pruneRolePermissions(t.Context(), tx, "tenant-1", []string{"permission-keep"}, "actor-1"))
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMemberRolesReturnsDisplayFieldsAndScopesTenant(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	service := &TenantAuthorizationService{db: sqlxDB}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", TenantID: "tenant-1", MembershipID: "member-actor",
	})
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships WHERE tenant_id=`).
		WithArgs("tenant-1", "member-target").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT r.id,r.code,r.name FROM tenant_member_roles`).
		WithArgs("tenant-1", "member-target").
		WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name"}).AddRow("role-1", "auditor", "审计员"))

	roles, err := service.MemberRoles(ctx, "member-target")
	require.NoError(t, err)
	require.Equal(t, []MemberRoleView{{ID: "role-1", Code: "auditor", Name: "审计员"}}, roles)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageRolesRejectsUnboundedAndInvalidFilters(t *testing.T) {
	service := &TenantAuthorizationService{}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", TenantID: "tenant-1", MembershipID: "member-1",
	})
	to := time.Now()
	from := to.Add(time.Hour)
	cases := []RolePageInput{
		{Request: pagination.Request{Page: 1}, IDs: make([]string, 201)},
		{Request: pagination.Request{Page: 1}, Statuses: []string{"unknown"}},
		{Request: pagination.Request{Page: 1}, CreatedAtFrom: &from, CreatedAtTo: &to},
	}
	for _, input := range cases {
		_, err := service.PageRoles(ctx, input)
		require.ErrorIs(t, err, ErrTenantAuthorizationInvalid)
	}
}

type immediateLocker struct{}

func (immediateLocker) TryLock(context.Context, string, time.Duration) (cache.Lock, bool, error) {
	return immediateLock{}, true, nil
}
func (immediateLocker) Lock(context.Context, string, time.Duration, time.Duration) (cache.Lock, error) {
	return immediateLock{}, nil
}

type immediateLock struct{}

func (immediateLock) Extend(context.Context) error { return nil }
func (immediateLock) Unlock(context.Context) error { return nil }
func (immediateLock) Until() time.Time             { return time.Now().Add(time.Minute) }

type operationRecorderStub struct{}

func (operationRecorderStub) Enabled() bool                                    { return true }
func (operationRecorderStub) Record(context.Context, operationlog.Entry) error { return nil }

func TestCreateRoleKeepsCallerContextAfterLeaseEnds(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := NewTenantAuthorizationService(
		db,
		database.NewTransactor(db),
		immediateLocker{},
		operationRecorderStub{},
		nil,
		config.Config{DistributedLock: config.DistributedLock{TTL: time.Second, RetryDelay: 10 * time.Millisecond}},
	)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", TenantID: "tenant-1", MembershipID: "member-1",
	})
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT g.permission_id FROM tenant_permission_grants`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"permission_id"}))
	mock.ExpectExec(`INSERT INTO tenant_roles`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE tenant_role_permissions`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT id,tenant_id,code,name,description,status`).
		WithArgs("tenant-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "code", "name", "description", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow("role-1", "tenant-1", "auditor", "Auditor", "", "active", now, "user-1", now, "user-1", 1))

	role, err := service.CreateRole(ctx, "auditor", "Auditor", "", nil)
	require.NoError(t, err)
	require.Equal(t, "role-1", role.ID)
	require.NoError(t, ctx.Err())
	require.NoError(t, mock.ExpectationsWereMet())
}
