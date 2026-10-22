package authorization

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	"github.com/lihongjie0209/go-api-template/internal/testutil"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestTenantAuthorizationLockDistinguishesCommitFromReleaseFailure(t *testing.T) {
	t.Parallel()
	callbackFailure := errors.New("transaction rolled back")
	for _, test := range []struct {
		name        string
		locker      testutil.LockStub
		callbackErr error
		want        error
		wantCalled  bool
	}{
		{name: "acquisition failure", locker: testutil.LockStub{AcquireErr: context.DeadlineExceeded}, want: context.DeadlineExceeded},
		{name: "release failure after commit", locker: testutil.LockStub{ReleaseErr: cache.ErrLockLost}, wantCalled: true},
		{name: "callback failure before commit", locker: testutil.LockStub{ReleaseErr: cache.ErrLockLost}, callbackErr: callbackFailure, want: callbackFailure, wantCalled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := &TenantAuthorizationService{locker: test.locker, cfg: config.Config{DistributedLock: config.DistributedLock{TTL: time.Second, RetryDelay: time.Millisecond}}}
			called := false
			err := service.withLock(t.Context(), "tenant:tenant-1:grant", func(context.Context) error {
				called = true
				return test.callbackErr
			})
			if test.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, test.want)
			}
			require.NotErrorIs(t, err, ErrTenantAuthorizationConflict)
			require.Equal(t, test.wantCalled, called)
		})
	}
}

func TestNormalizeIDs(t *testing.T) {
	actual, err := normalizeIDs([]string{"b", "a", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, actual)
	_, err = normalizeIDs([]string{string(make([]byte, maxAuthorizationIDLength+1))})
	require.ErrorIs(t, err, ErrTenantAuthorizationInvalid)
	_, err = normalizeIDs(make([]string, 1001))
	require.ErrorIs(t, err, ErrTenantAuthorizationInvalid)
}

func TestSetAdministratorRejectsMissingVersionBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()
	service := &TenantAuthorizationService{}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "actor-1", Type: platformprincipal.TypeSystem})
	require.ErrorIs(t, service.SetAdministrator(ctx, "tenant-1", "member-1", true, 0), ErrTenantAuthorizationInvalid)
}

func TestSetAdministratorPropagatesAdministratorLookupFailure(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, raw.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	lookupErr := errors.New("database unavailable")
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators a JOIN tenant_memberships m`).
		WithArgs("tenant-1", "actor-membership").WillReturnError(lookupErr)
	mock.ExpectClose()
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "actor-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "actor-membership"})
	err = service.SetAdministrator(ctx, "tenant-1", "target-membership", true, 1)
	require.ErrorIs(t, err, lookupErr)
	require.NotErrorIs(t, err, ErrTenantAuthorizationForbidden)
}

func TestAdvanceAdministratorMembershipVersion(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		rows    int64
		wantErr error
	}{
		{name: "updated", rows: 1},
		{name: "stale or foreign tenant", rows: 0, wantErr: ErrTenantAuthorizationStaleVersion},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			mock.ExpectBegin()
			tx, err := sqlx.NewDb(raw, "sqlmock").Beginx()
			require.NoError(t, err)
			mock.ExpectExec(`UPDATE tenant_memberships SET updated_at=\?,updated_by=\?,version=version\+1 WHERE tenant_id=\? AND id=\? AND version=\? AND status='active' AND deleted_at IS NULL`).
				WithArgs(sqlmock.AnyArg(), "actor-1", "tenant-1", "member-1", int64(3)).
				WillReturnResult(sqlmock.NewResult(0, test.rows))
			err = advanceAdministratorMembershipVersion(t.Context(), tx, "tenant-1", "member-1", "actor-1", 3)
			if test.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, test.wantErr)
			}
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

type actorResolverStub struct {
	calls int
	ids   []string
}

type unavailableRoleActors struct{}

func (unavailableRoleActors) ResolveUserIDs(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("identity unavailable")
}

func tenantRoleRows(roleID, name string, version int64) *sqlmock.Rows {
	stamp := time.Date(2026, time.October, 22, 0, 0, 0, 0, time.UTC)
	return sqlmock.NewRows([]string{"id", "tenant_id", "code", "name", "description", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow(roleID, "tenant-1", "auditor", name, "", "active", stamp, "creator-1", stamp, "actor-1", version)
}

func (s *actorResolverStub) ResolveUserIDs(_ context.Context, ids []string) (map[string]string, error) {
	s.calls++
	s.ids = append([]string(nil), ids...)
	return map[string]string{"user-1": "Alice"}, nil
}

func TestRolePresentationBatchesActorsAndUsesPlatformTimezone(t *testing.T) {
	t.Parallel()
	resolver := &actorResolverStub{}
	service := &TenantAuthorizationService{actors: resolver}
	instant := time.Date(2026, time.September, 16, 1, 2, 3, 0, time.UTC)
	roles := []TenantRole{
		{CreatedBy: "user-1", UpdatedBy: "system-1", CreatedAt: instant, UpdatedAt: instant},
		{CreatedBy: "user-1", UpdatedBy: "user-1", CreatedAt: instant, UpdatedAt: instant},
	}

	require.NoError(t, service.presentRoles(t.Context(), roles))
	require.Equal(t, 1, resolver.calls)
	require.Equal(t, "Alice", roles[0].CreatedByName)
	require.Equal(t, "system-1", roles[0].UpdatedByName)
	require.Equal(t, "2026-09-16T09:02:03+08:00", roles[0].CreatedAt.Format(time.RFC3339))
}

func TestTenantRoleScopeFailsClosedWithoutCompiler(t *testing.T) {
	t.Parallel()
	service := &TenantAuthorizationService{}
	userContext := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"})
	_, err := service.roleScope(userContext)
	require.ErrorIs(t, err, datapermission.ErrScopeRequired)

	systemContext := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1"})
	scope, err := service.roleScope(systemContext)
	require.NoError(t, err)
	require.Equal(t, "(1 = 1)", scope.Clause)
}

func TestGetRoleAppliesTenantAndDataPermissionInOneQuery(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	now := time.Now()
	mock.ExpectQuery(`SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status,tr.created_at,tr.created_by,tr.updated_at,tr.updated_by,tr.version FROM tenant_roles tr WHERE tr.tenant_id=\? AND tr.id=\? AND tr.deleted_at IS NULL AND \(tr.created_by = \?\)`).
		WithArgs("tenant-1", "role-1", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "code", "name", "description", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow("role-1", "tenant-1", "auditor", "Auditor", "", "active", now, "user-1", now, "user-1", 1))
	service := &TenantAuthorizationService{db: db}
	record, err := service.getRole(t.Context(), "tenant-1", "role-1", datapermission.SQLPredicate{Clause: "(tr.created_by = ?)", Args: []any{"user-1"}})
	require.NoError(t, err)
	require.Equal(t, "role-1", record.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageRolesAppliesIdenticalDataScopeToCountAndItems(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	scope := datapermission.SQLPredicate{Clause: "(tr.created_by = ?)", Args: []any{"user-1"}}
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_roles tr WHERE tr.tenant_id=\? AND tr.deleted_at IS NULL AND \(tr.created_by = \?\)`).
		WithArgs("tenant-1", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT .* FROM tenant_roles tr WHERE tr.tenant_id=\? AND tr.deleted_at IS NULL AND \(tr.created_by = \?\).*LIMIT \? OFFSET \?`).
		WithArgs("tenant-1", "user-1", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "code", "name", "description", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}))
	service := &TenantAuthorizationService{db: db}
	request := pagination.Request{Page: 1, PageSize: 20}
	items, total, err := service.pageRoles(t.Context(), "tenant-1", RolePageInput{}, request, scope)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, items)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageRoleAssignmentCandidatesChecksEveryPermission(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1"})
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id FROM tenant_role_permissions`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("perm-read"))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_roles tr`).WithArgs("tenant-1", "active").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))
	now := time.Now()
	mock.ExpectQuery(`SELECT tr.id,tr.tenant_id.*FROM tenant_roles tr`).WithArgs("tenant-1", "active", 20, 0).WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "code", "name", "description", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow("role-1", "tenant-1", "reader", "Reader", "", "active", now, "user-1", now, "user-1", 1).
		AddRow("role-2", "tenant-1", "writer", "Writer", "", "active", now, "user-1", now, "user-1", 1).
		AddRow("role-3", "tenant-1", "empty", "Empty", "", "active", now, "user-1", now, "user-1", 1).
		AddRow("role-4", "tenant-1", "revoked", "Revoked", "", "active", now, "user-1", now, "user-1", 1))
	mock.ExpectQuery(`SELECT rp.role_id,rp.permission_id,CASE WHEN`).WithArgs("tenant-1", "role-1", "role-2", "role-3", "role-4").WillReturnRows(sqlmock.NewRows([]string{"role_id", "permission_id", "valid"}).
		AddRow("role-1", "perm-read", 1).
		AddRow("role-2", "perm-write", 1).
		AddRow("role-4", "perm-read", 0))
	service := &TenantAuthorizationService{db: db}
	page, err := service.PageRoleAssignmentCandidates(ctx, RolePageInput{Request: pagination.Request{Page: 1, PageSize: 20}})
	require.NoError(t, err)
	require.Equal(t, int64(4), page.Total)
	require.True(t, page.Items[0].Assignable)
	require.False(t, page.Items[1].Assignable)
	require.True(t, page.Items[2].Assignable)
	require.False(t, page.Items[3].Assignable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageRoleAssignmentCandidatesRejectsInactiveMember(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1"})
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	_, err = service.PageRoleAssignmentCandidates(ctx, RolePageInput{Request: pagination.Request{Page: 1, PageSize: 20}})
	require.ErrorIs(t, err, ErrTenantAuthorizationForbidden)
	require.NoError(t, mock.ExpectationsWereMet())
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

func TestEffectivePermissionsReturnsDisplayContractForCurrentMember(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1",
	})
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id FROM tenant_role_permissions`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("permission-read"))
	mock.ExpectQuery(`SELECT p.id,p.permission_key,p.name,p.resource,p.action FROM permissions p WHERE p.id IN \(\?\)`).
		WithArgs("permission-read").
		WillReturnRows(sqlmock.NewRows([]string{"id", "permission_key", "name", "resource", "action"}).
			AddRow("permission-read", "tenant.member.read", "查看成员", "tenant.member", "read"))

	permissions, err := service.EffectivePermissions(ctx, "")
	require.NoError(t, err)
	require.Equal(t, []PermissionView{{ID: "permission-read", PermissionKey: "tenant.member.read", Name: "查看成员", Resource: "tenant.member", Action: "read"}}, permissions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAssignablePermissionsReturnsDisplayFieldsWithinCallerCeiling(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	service := &TenantAuthorizationService{db: sqlxDB}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1",
	})
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id FROM tenant_role_permissions`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("permission-read"))
	mock.ExpectQuery(`SELECT p.id,p.permission_key,p.name,p.resource,p.action FROM permissions p WHERE p.id IN \(\?\)`).
		WithArgs("permission-read").
		WillReturnRows(sqlmock.NewRows([]string{"id", "permission_key", "name", "resource", "action"}).
			AddRow("permission-read", "tenant.member.read", "查看成员", "tenant.member", "read"))

	permissions, err := service.AssignablePermissions(ctx)
	require.NoError(t, err)
	require.Equal(t, []PermissionView{{ID: "permission-read", PermissionKey: "tenant.member.read", Name: "查看成员", Resource: "tenant.member", Action: "read"}}, permissions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAssignablePermissionsReturnsEmptyWithoutBuildingINQuery(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	service := &TenantAuthorizationService{db: sqlxDB}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1",
	})
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id FROM tenant_role_permissions`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"permission_id"}))

	permissions, err := service.AssignablePermissions(ctx)
	require.NoError(t, err)
	require.Empty(t, permissions)
	require.NotNil(t, permissions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageAssignablePermissionsUsesTenantCeilingAndRoleScope(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		admin     int
		countArgs []driver.Value
		countSQL  string
	}{
		{"administrator", 1, []driver.Value{"tenant-1", "%read%", "%read%"}, `SELECT count\(\*\) FROM permissions p WHERE .*r.scope='tenant'.*tenant_permission_grants.*LOWER`},
		{"member", 0, []driver.Value{"tenant-1", "member-1", "tenant-1", "%read%", "%read%"}, `SELECT count\(\*\) FROM permissions p WHERE .*r.scope='tenant'.*tenant_role_permissions.*LOWER`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			service := &TenantAuthorizationService{db: sqlx.NewDb(db, "sqlmock")}
			ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
				ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1",
			})
			mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(test.admin))
			mock.ExpectQuery(test.countSQL).WithArgs(test.countArgs...).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			pageArgs := append(append([]driver.Value{}, test.countArgs...), 20, 0)
			mock.ExpectQuery(`SELECT p.id,p.permission_key,p.name,p.resource,p.action FROM permissions p WHERE .*r.scope='tenant'.* ORDER BY p.permission_key,p.id LIMIT \? OFFSET \?`).WithArgs(pageArgs...).WillReturnRows(sqlmock.NewRows([]string{"id", "permission_key", "name", "resource", "action"}).AddRow("permission-read", "tenant.member.read", "Read", "tenant.member", "read"))
			page, err := service.PageAssignablePermissions(ctx, AssignablePermissionPageInput{Request: pagination.Request{Keyword: "read"}})
			require.NoError(t, err)
			require.Equal(t, int64(1), page.Total)
			require.Equal(t, "permission-read", page.Items[0].ID)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPageAssignablePermissionsRejectsInactiveMemberAndUnboundedIDs(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service := &TenantAuthorizationService{db: sqlx.NewDb(db, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1",
	})
	_, err = service.PageAssignablePermissions(ctx, AssignablePermissionPageInput{IDs: make([]string, 201)})
	require.ErrorIs(t, err, ErrTenantAuthorizationInvalid)
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	_, err = service.PageAssignablePermissions(ctx, AssignablePermissionPageInput{})
	require.ErrorIs(t, err, ErrTenantAuthorizationForbidden)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageAssignablePermissionsFiltersSelectedIDsBeforeCounting(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service := &TenantAuthorizationService{db: sqlx.NewDb(db, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1",
	})
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM permissions p WHERE .*p.id IN \(\?, \?\)`).WithArgs("tenant-1", "permission-a", "permission-b").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT p.id,p.permission_key,p.name,p.resource,p.action FROM permissions p WHERE .*p.id IN \(\?, \?\).*LIMIT \? OFFSET \?`).WithArgs("tenant-1", "permission-a", "permission-b", 200, 0).WillReturnRows(sqlmock.NewRows([]string{"id", "permission_key", "name", "resource", "action"}).AddRow("permission-a", "tenant.member.read", "Read", "tenant.member", "read"))
	page, err := service.PageAssignablePermissions(ctx, AssignablePermissionPageInput{
		Request: pagination.Request{PageSize: 200}, IDs: []string{"permission-a", "permission-b"},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, []PermissionView{{ID: "permission-a", PermissionKey: "tenant.member.read", Name: "Read", Resource: "tenant.member", Action: "read"}}, page.Items)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantPermissionCeilingReturnsAuthoritativePlatformGrantSet(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := &TenantAuthorizationService{db: db}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "platform-user", Type: platformprincipal.TypeUser})
	mock.ExpectQuery(`SELECT name FROM tenants WHERE id=\? AND deleted_at IS NULL`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Tenant One"))
	mock.ExpectQuery(`(?s)SELECT p.id,p.permission_key,p.name,p.resource,p.action,\(.*r.scope='tenant'.*\) AS assignable FROM tenant_permission_grants g JOIN permissions p`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "permission_key", "name", "resource", "action", "assignable"}).
			AddRow("permission-read", "tenant.member.read", "查看成员", "tenant.member", "read", true).
			AddRow("permission-old", "tenant.old.read", "旧权限", "tenant.old", "read", false))

	permissions, err := service.TenantPermissionCeiling(ctx, "tenant-1")
	require.NoError(t, err)
	require.Equal(t, []TenantCeilingPermission{
		{ID: "permission-read", PermissionKey: "tenant.member.read", Name: "查看成员", Resource: "tenant.member", Action: "read", Assignable: true},
		{ID: "permission-old", PermissionKey: "tenant.old.read", Name: "旧权限", Resource: "tenant.old", Action: "read", Assignable: false},
	}, permissions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantPermissionCeilingRejectsTenantPrincipalBeforeDatabase(t *testing.T) {
	t.Parallel()
	service := &TenantAuthorizationService{}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	_, err := service.TenantPermissionCeiling(ctx, "tenant-1")
	require.ErrorIs(t, err, ErrTenantAuthorizationForbidden)
}

func TestTenantPermissionTargetReturnsNameAndCurrentVersion(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "platform-user", Type: platformprincipal.TypeUser})
	mock.ExpectQuery(`SELECT id,name,version FROM tenants WHERE id=\? AND deleted_at IS NULL`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "version"}).AddRow("tenant-1", "Tenant One", 7))
	target, err := service.TenantPermissionTarget(ctx, " tenant-1 ")
	require.NoError(t, err)
	require.Equal(t, TenantPermissionTarget{ID: "tenant-1", Name: "Tenant One", Version: 7}, target)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantPermissionTargetRejectsInvalidAndTenantContextBeforeDatabase(t *testing.T) {
	t.Parallel()
	service := &TenantAuthorizationService{}
	platformCtx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "platform-user", Type: platformprincipal.TypeUser})
	for _, tenantID := range []string{"", strings.Repeat("x", maxAuthorizationIDLength+1)} {
		_, err := service.TenantPermissionTarget(platformCtx, tenantID)
		require.ErrorIs(t, err, ErrTenantAuthorizationInvalid)
	}
	tenantCtx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "tenant-user", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	_, err := service.TenantPermissionTarget(tenantCtx, "tenant-1")
	require.ErrorIs(t, err, ErrTenantAuthorizationForbidden)
}

func TestTenantPermissionTargetHandlesMissingAndDatabaseFailure(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "platform-user", Type: platformprincipal.TypeUser})
	query := `SELECT id,name,version FROM tenants WHERE id=\? AND deleted_at IS NULL`
	mock.ExpectQuery(query).WithArgs("deleted-tenant").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "version"}))
	_, err = service.TenantPermissionTarget(ctx, "deleted-tenant")
	require.ErrorIs(t, err, ErrTenantAuthorizationNotFound)
	mock.ExpectQuery(query).WithArgs("tenant-2").WillReturnError(errors.New("database unavailable"))
	_, err = service.TenantPermissionTarget(ctx, "tenant-2")
	require.ErrorContains(t, err, "load tenant permission target: database unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageTenantGrantablePermissionsScopesCountAndItems(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.SystemContext(t.Context(), "platform-admin")
	mock.ExpectQuery(`(?s)SELECT count\(\*\) FROM permissions p WHERE .*r.scope='tenant'.*LOWER\(p.permission_key\)`).
		WithArgs("%member%", "%member%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT p.id,p.permission_key,p.name,p.resource,p.action FROM permissions p WHERE .*r.scope='tenant'.*ORDER BY p.permission_key,p.id LIMIT \? OFFSET \?`).
		WithArgs("%member%", "%member%", 20, 20).
		WillReturnRows(sqlmock.NewRows([]string{"id", "permission_key", "name", "resource", "action"}).
			AddRow("permission-read", "tenant.member.read", "查看成员", "tenant.member", "read"))

	page, err := service.PageTenantGrantablePermissions(ctx, pagination.Request{Page: 2, Keyword: "member"})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, 2, page.Page)
	require.Equal(t, 20, page.PageSize)
	require.Equal(t, []PermissionView{{ID: "permission-read", PermissionKey: "tenant.member.read", Name: "查看成员", Resource: "tenant.member", Action: "read"}}, page.Items)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageTenantGrantablePermissionsRejectsInvalidAndTenantActor(t *testing.T) {
	t.Parallel()
	service := &TenantAuthorizationService{}
	platform := platformprincipal.SystemContext(t.Context(), "platform-admin")
	for _, input := range []pagination.Request{{Page: -1}, {PageSize: pagination.MaxPageSize + 1}, {Keyword: string(make([]byte, 257))}} {
		_, err := service.PageTenantGrantablePermissions(platform, input)
		require.ErrorIs(t, err, ErrTenantAuthorizationInvalid)
	}
	tenant := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	_, err := service.PageTenantGrantablePermissions(tenant, pagination.Request{})
	require.ErrorIs(t, err, ErrTenantAuthorizationForbidden)
}

func TestPageTenantGrantablePermissionsPropagatesDatabaseFailure(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	databaseError := errors.New("database unavailable")
	mock.ExpectQuery(`SELECT count\(\*\) FROM permissions p WHERE`).WillReturnError(databaseError)
	_, err = service.PageTenantGrantablePermissions(platformprincipal.SystemContext(t.Context(), "platform-admin"), pagination.Request{})
	require.ErrorIs(t, err, databaseError)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageAdministratorCandidatesUsesTenantIsolationForCountAndItems(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := &TenantAuthorizationService{db: db}
	request := pagination.Request{Page: 1, PageSize: 20}
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants t ON t.id=m.tenant_id AND t.deleted_at IS NULL LEFT JOIN tenant_administrators a .* WHERE m.tenant_id=\? AND m.deleted_at IS NULL`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT m.id AS membership_id,.* FROM tenant_memberships m JOIN tenants t .* WHERE m.tenant_id=\? AND m.deleted_at IS NULL ORDER BY m.joined_at DESC,m.id LIMIT \? OFFSET \?`).
		WithArgs("tenant-1", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"membership_id", "tenant_id", "user_id", "username", "display_name", "status", "joined_at", "version", "is_administrator"}).
			AddRow("membership-1", "tenant-1", "user-1", "alice", "Alice", "active", time.Now(), 3, true))

	items, total, err := service.pageAdministratorCandidates(t.Context(), "tenant-1", AdministratorPageInput{}, request)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	require.True(t, items[0].IsAdministrator)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPagePlatformAdministratorCandidatesRejectsTenantPrincipalBeforeDatabase(t *testing.T) {
	t.Parallel()
	service := &TenantAuthorizationService{}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	_, err := service.PagePlatformAdministratorCandidates(ctx, "tenant-1", AdministratorPageInput{})
	require.ErrorIs(t, err, ErrTenantAuthorizationForbidden)
}

func TestPageTenantAdministratorCandidatesRejectsInvalidPrincipalBeforeDatabase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		principal platformprincipal.Principal
	}{
		{name: "platform principal", principal: platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}},
		{name: "missing membership", principal: platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &TenantAuthorizationService{}
			ctx := platformprincipal.WithContext(t.Context(), test.principal)
			_, err := service.PageTenantAdministratorCandidates(ctx, AdministratorPageInput{})
			require.ErrorIs(t, err, ErrTenantAuthorizationForbidden)
		})
	}
}

func TestPageTenantAdministratorCandidatesRejectsNonAdministratorBeforeCandidateQuery(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"})
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators a JOIN tenant_memberships m .* JOIN tenants t .* WHERE a.tenant_id=\? AND a.membership_id=\? AND a.deleted_at IS NULL`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	_, err = service.PageTenantAdministratorCandidates(ctx, AdministratorPageInput{})
	require.ErrorIs(t, err, ErrTenantAuthorizationForbidden)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageTenantAdministratorCandidatesUsesOnlyPrincipalTenant(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"})
	joinedAt := time.Date(2026, time.September, 18, 1, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators a JOIN tenant_memberships m .* JOIN tenants t .* WHERE a.tenant_id=\? AND a.membership_id=\? AND a.deleted_at IS NULL`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants t .* WHERE m.tenant_id=\? AND m.deleted_at IS NULL`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT m.id AS membership_id,.* WHERE m.tenant_id=\? AND m.deleted_at IS NULL ORDER BY m.joined_at DESC,m.id LIMIT \? OFFSET \?`).
		WithArgs("tenant-1", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"membership_id", "tenant_id", "user_id", "username", "display_name", "status", "joined_at", "version", "is_administrator"}).
			AddRow("member-2", "tenant-1", "user-2", "alice", "Alice", "active", joinedAt, 2, false))

	page, err := service.PageTenantAdministratorCandidates(ctx, AdministratorPageInput{})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.Len(t, page.Items, 1)
	require.Equal(t, "tenant-1", page.Items[0].TenantID)
	require.Equal(t, "2026-09-18T09:00:00+08:00", page.Items[0].JoinedAt.Format(time.RFC3339))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageTenantAdministratorCandidatesPropagatesGuardDatabaseFailure(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"})
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).
		WithArgs("tenant-1", "member-1").
		WillReturnError(context.DeadlineExceeded)

	_, err = service.PageTenantAdministratorCandidates(ctx, AdministratorPageInput{})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, ErrTenantAuthorizationForbidden)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRequireSubset(t *testing.T) {
	require.NoError(t, requireSubset([]string{"read"}, []string{"write", "read"}))
	require.ErrorIs(t, requireSubset([]string{"delete"}, []string{"read"}), ErrTenantAuthorizationForbidden)
	require.True(t, errors.Is(requireSubset([]string{"delete"}, nil), ErrTenantAuthorizationForbidden))
}

func TestEnsureRoleAssignableRejectsRoleAboveCallerCeiling(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	mock.ExpectBegin()
	tx, err := sqlxDB.Beginx()
	require.NoError(t, err)
	mock.ExpectQuery(`SELECT permission_id FROM tenant_role_permissions`).WithArgs("tenant-1", "role-1").WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("permission-delete"))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_roles`).WithArgs("tenant-1", "role-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT p.id FROM permissions p WHERE .*r.scope='tenant'`).WithArgs("permission-delete").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("permission-delete"))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id FROM tenant_role_permissions`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("permission-read"))

	actor := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"}
	service := &TenantAuthorizationService{}
	require.ErrorIs(t, service.ensureRoleAssignable(t.Context(), tx, actor, "role-1"), ErrTenantAuthorizationForbidden)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEnsureRolesAssignablePropagatesRoleCountFailure(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectBegin()
	tx, err := db.BeginTxx(t.Context(), nil)
	require.NoError(t, err)
	mock.ExpectQuery(`SELECT DISTINCT permission_id FROM tenant_role_permissions`).
		WithArgs("tenant-1", "role-1").
		WillReturnRows(sqlmock.NewRows([]string{"permission_id"}))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_roles`).
		WithArgs("tenant-1", "role-1").
		WillReturnError(context.DeadlineExceeded)
	actor := platformprincipal.Principal{TenantID: "tenant-1", MembershipID: "member-1"}
	err = (&TenantAuthorizationService{}).ensureRolesAssignable(t.Context(), tx, actor, []string{"role-1"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, ErrTenantAuthorizationInvalid)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSetRolePermissionsCanRemoveNoLongerAssignablePermission(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := &TenantAuthorizationService{db: db, transactor: database.NewTransactor(db), actors: unavailableRoleActors{}}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "actor-1", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1",
	})
	mock.ExpectQuery(`SELECT tr.id,tr.tenant_id.*FROM tenant_roles tr`).
		WithArgs("tenant-1", "role-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name"}).AddRow("role-1", "tenant-1", "角色"))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id FROM tenant_role_permissions`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"permission_id"}))
	mock.ExpectExec(`UPDATE tenant_roles AS tr SET version=version\+1`).
		WithArgs(sqlmock.AnyArg(), "actor-1", "tenant-1", "role-1", int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE tenant_role_permissions SET deleted_at=`).
		WithArgs(sqlmock.AnyArg(), "actor-1", sqlmock.AnyArg(), "actor-1", "tenant-1", "role-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, service.SetRolePermissions(ctx, "role-1", 2, nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDeleteRoleDoesNotRequireActorDisplayResolver(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := &TenantAuthorizationService{db: db, transactor: database.NewTransactor(db), actors: unavailableRoleActors{}}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "actor-1", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1",
	})
	want := errors.New("permission lookup unavailable")
	mock.ExpectQuery(`SELECT tr.id,tr.tenant_id.*FROM tenant_roles tr`).WithArgs("tenant-1", "role-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name"}).AddRow("role-1", "tenant-1", "Auditor"))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT permission_id FROM tenant_role_permissions`).WithArgs("tenant-1", "role-1").WillReturnError(want)
	mock.ExpectRollback()
	err = service.DeleteRole(ctx, "role-1", 1)
	require.ErrorIs(t, err, want)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSetRolePermissionsReportsStaleVersion(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := &TenantAuthorizationService{db: db, transactor: database.NewTransactor(db)}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "actor-1", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1",
	})
	mock.ExpectQuery(`SELECT tr.id,tr.tenant_id.*FROM tenant_roles tr`).
		WithArgs("tenant-1", "role-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name"}).AddRow("role-1", "tenant-1", "角色"))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id FROM tenant_role_permissions`).
		WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"permission_id"}))
	mock.ExpectExec(`UPDATE tenant_roles AS tr SET version=version\+1`).
		WithArgs(sqlmock.AnyArg(), "actor-1", "tenant-1", "role-1", int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err = service.SetRolePermissions(ctx, "role-1", 2, nil)
	require.ErrorIs(t, err, ErrTenantAuthorizationStaleVersion)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestValidateTenantPermissionsRejectsPlatformResource(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	txDB := sqlx.NewDb(db, "sqlmock")
	mock.ExpectBegin()
	tx, err := txDB.Beginx()
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)SELECT p.id FROM permissions p WHERE .*r.scope='tenant'`).WithArgs("permission-platform").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	service := &TenantAuthorizationService{}
	require.ErrorIs(t, service.validateTenantPermissions(t.Context(), tx, []string{"permission-platform"}), ErrTenantAuthorizationInvalid)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestValidateTenantPermissionsRejectsDisabledPermission(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectBegin()
	tx, err := db.Beginx()
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)SELECT p.id FROM permissions p WHERE .*p.status='active'`).
		WithArgs("permission-disabled").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	service := &TenantAuthorizationService{}
	require.ErrorIs(t, service.validateTenantPermissions(t.Context(), tx, []string{"permission-disabled"}), ErrTenantAuthorizationInvalid)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestValidateTenantPermissionsAcceptsDatabaseRegisteredManualTenantAction(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectBegin()
	tx, err := sqlx.NewDb(raw, "sqlmock").Beginx()
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)SELECT p.id FROM permissions p WHERE .*r.scope='tenant'`).
		WithArgs("manual-permission").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("manual-permission"))
	service := &TenantAuthorizationService{}
	require.NoError(t, service.validateTenantPermissions(t.Context(), tx, []string{"manual-permission"}))
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
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
		ID: "user-1", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-actor",
	})
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships tm WHERE tm.tenant_id=`).
		WithArgs("tenant-1", "member-target").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT r.id,r.code,r.name,r.status FROM tenant_member_roles mr JOIN tenant_roles r ON r.tenant_id=mr.tenant_id AND r.id=mr.role_id AND r.deleted_at IS NULL WHERE mr.tenant_id=`).
		WithArgs("tenant-1", "member-target").
		WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "status"}).AddRow("role-1", "auditor", "审计员", "disabled"))

	roles, err := service.MemberRoles(ctx, "member-target")
	require.NoError(t, err)
	require.Equal(t, []MemberRoleView{{ID: "role-1", Code: "auditor", Name: "审计员", Status: "disabled"}}, roles)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRolePermissionsIncludesInactiveLinkedPermission(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-actor",
	})
	mock.ExpectQuery(`SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status,tr.created_at,tr.created_by,tr.updated_at,tr.updated_by,tr.version FROM tenant_roles tr`).
		WithArgs("tenant-1", "role-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("role-1"))
	mock.ExpectQuery(`SELECT p.id,p.permission_key,p.name,p.resource,p.action,p.status FROM tenant_role_permissions rp JOIN permissions p ON p.id=rp.permission_id AND p.node_type='permission' AND p.deleted_at IS NULL WHERE rp.tenant_id=`).
		WithArgs("tenant-1", "role-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "permission_key", "name", "resource", "action", "status"}).
			AddRow("permission-1", "tenant.member.read", "查看成员", "tenant.member", "read", "disabled"))

	permissions, err := service.RolePermissions(ctx, "role-1")
	require.NoError(t, err)
	require.Equal(t, []PermissionView{{ID: "permission-1", PermissionKey: "tenant.member.read", Name: "查看成员", Resource: "tenant.member", Action: "read", Status: "disabled"}}, permissions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageRolesRejectsUnboundedAndInvalidFilters(t *testing.T) {
	service := &TenantAuthorizationService{}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1",
	})
	to := time.Now()
	from := to.Add(time.Hour)
	cases := []RolePageInput{
		{Request: pagination.Request{Page: 1}, IDs: make([]string, 201)},
		{Request: pagination.Request{Page: 1}, Keyword: string(make([]byte, maxRoleKeywordLength+1))},
		{Request: pagination.Request{Page: 1}, IDs: []string{""}},
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
func (operationRecorderStub) RecordTx(context.Context, *sqlx.Tx, operationlog.Entry) error {
	return nil
}

type authorizationOperationRecorder struct {
	standalone []operationlog.Entry
	txEntries  []operationlog.Entry
}

func (*authorizationOperationRecorder) Enabled() bool { return true }
func (r *authorizationOperationRecorder) Record(_ context.Context, entry operationlog.Entry) error {
	r.standalone = append(r.standalone, entry)
	return nil
}
func (r *authorizationOperationRecorder) RecordTx(_ context.Context, _ *sqlx.Tx, entry operationlog.Entry) error {
	r.txEntries = append(r.txEntries, entry)
	return nil
}

type authorizationSecurityRecorder struct {
	txErr      error
	standalone []securitylog.Entry
	txEntries  []securitylog.Entry
}

func (*authorizationSecurityRecorder) Enabled() bool    { return true }
func (*authorizationSecurityRecorder) FailClosed() bool { return true }
func (r *authorizationSecurityRecorder) Record(_ context.Context, entry securitylog.Entry) error {
	r.standalone = append(r.standalone, entry)
	return nil
}
func (r *authorizationSecurityRecorder) RecordTx(_ context.Context, _ *sqlx.Tx, entry securitylog.Entry) error {
	r.txEntries = append(r.txEntries, entry)
	return r.txErr
}

func TestAuthorizationMutationRollsBackWhenTransactionalSecurityLogFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	operations := &authorizationOperationRecorder{}
	wantErr := errors.New("security outbox unavailable")
	security := &authorizationSecurityRecorder{txErr: wantErr}
	service := &TenantAuthorizationService{transactor: database.NewTransactor(db), operations: operations, security: security}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE tenant_roles SET name`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"})

	err = service.mutate(ctx, "tenant.role.update", "role-1", map[string]any{"name": "Auditor"}, "tenant-1", nil, func(tx *sqlx.Tx) error {
		_, execErr := tx.ExecContext(ctx, `UPDATE tenant_roles SET name='Auditor'`)
		return execErr
	})
	require.ErrorIs(t, err, wantErr)
	require.Len(t, operations.txEntries, 1)
	require.Len(t, security.txEntries, 1)
	require.Len(t, operations.standalone, 1)
	require.Len(t, security.standalone, 1)
	require.False(t, operations.standalone[0].Succeeded)
	require.NoError(t, mock.ExpectationsWereMet())
}

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
		nil,
		config.Config{DistributedLock: config.DistributedLock{TTL: time.Second, RetryDelay: 10 * time.Millisecond}},
		nil,
	)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1",
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
	mock.ExpectQuery(`SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status`).
		WithArgs("tenant-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "code", "name", "description", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow("role-1", "tenant-1", "auditor", "Auditor", "", "active", now, "user-1", now, "user-1", 1))
	mock.ExpectCommit()

	role, err := service.CreateRole(ctx, "auditor", "Auditor", "", nil)
	require.NoError(t, err)
	require.Equal(t, "role-1", role.ID)
	require.NoError(t, ctx.Err())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCreateRoleRollsBackWhenAuditedReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := NewTenantAuthorizationService(db, database.NewTransactor(db), nil, nil, nil, nil, config.Config{}, nil)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "actor-1", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1"})
	want := errors.New("read role unavailable")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT g.permission_id FROM tenant_permission_grants`).WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"permission_id"}))
	mock.ExpectExec(`INSERT INTO tenant_roles`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE tenant_role_permissions`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status`).WithArgs("tenant-1", sqlmock.AnyArg()).WillReturnError(want)
	mock.ExpectRollback()
	_, err = service.CreateRole(ctx, "auditor", "Auditor", "", nil)
	require.ErrorIs(t, err, want)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCreateRoleSurvivesActorDisplayOutage(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := NewTenantAuthorizationService(db, database.NewTransactor(db), nil, nil, nil, unavailableRoleActors{}, config.Config{}, nil)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "actor-1", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1"})
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).WithArgs("tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT g.permission_id FROM tenant_permission_grants`).WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"permission_id"}))
	mock.ExpectExec(`INSERT INTO tenant_roles`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE tenant_role_permissions`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status`).WithArgs("tenant-1", sqlmock.AnyArg()).
		WillReturnRows(tenantRoleRows("role-1", "Auditor", 1))
	mock.ExpectCommit()
	role, err := service.CreateRole(ctx, "auditor", "Auditor", "", nil)
	require.NoError(t, err)
	require.Equal(t, "role-1", role.ID)
	require.Equal(t, "creator-1", role.CreatedByName)
	require.Equal(t, "actor-1", role.UpdatedByName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateRoleReadsAuditedResultBeforeCommit(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		readErr error
		version int64
	}{
		{name: "success", version: 2},
		{name: "read failure rolls back", readErr: errors.New("read role unavailable"), version: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			db := sqlx.NewDb(raw, "sqlmock")
			service := NewTenantAuthorizationService(db, database.NewTransactor(db), nil, nil, nil, unavailableRoleActors{}, config.Config{}, nil)
			ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "actor-1", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1"})
			mock.ExpectQuery(`SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status`).WithArgs("tenant-1", "role-1").
				WillReturnRows(tenantRoleRows("role-1", "Auditor", 1))
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT permission_id FROM tenant_role_permissions`).WithArgs("tenant-1", "role-1").
				WillReturnRows(sqlmock.NewRows([]string{"permission_id"}))
			mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_roles`).WithArgs("tenant-1", "role-1").
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "member-1").
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).WithArgs("tenant-1", "member-1").
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			mock.ExpectQuery(`SELECT g.permission_id FROM tenant_permission_grants`).WithArgs("tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"permission_id"}))
			mock.ExpectExec(`UPDATE tenant_roles AS tr SET name=`).
				WithArgs("Senior Auditor", "", "active", sqlmock.AnyArg(), "actor-1", "tenant-1", "role-1", int64(1)).
				WillReturnResult(sqlmock.NewResult(0, 1))
			result := mock.ExpectQuery(`SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status`).WithArgs("tenant-1", "role-1")
			if tt.readErr != nil {
				result.WillReturnError(tt.readErr)
				mock.ExpectRollback()
			} else {
				result.WillReturnRows(tenantRoleRows("role-1", "Senior Auditor", 2))
				mock.ExpectCommit()
			}
			role, err := service.UpdateRole(ctx, "role-1", "Senior Auditor", "", "active", 1)
			if tt.readErr != nil {
				require.ErrorIs(t, err, tt.readErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, "Senior Auditor", role.Name)
				require.Equal(t, tt.version, role.Version)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
