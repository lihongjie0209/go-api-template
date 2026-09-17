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
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestNormalizeIDs(t *testing.T) {
	actual, err := normalizeIDs([]string{"b", "a", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, actual)
	_, err = normalizeIDs([]string{string(make([]byte, maxAuthorizationIDLength+1))})
	require.ErrorIs(t, err, ErrTenantAuthorizationInvalid)
	_, err = normalizeIDs(make([]string, 1001))
	require.ErrorIs(t, err, ErrTenantAuthorizationInvalid)
}

type actorResolverStub struct {
	calls int
	ids   []string
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
	mock.ExpectQuery(`SELECT p.id,p.permission_key,p.name,p.resource,p.action FROM tenant_permission_grants g JOIN permissions p`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "permission_key", "name", "resource", "action"}).
			AddRow("permission-read", "tenant.member.read", "查看成员", "tenant.member", "read"))

	permissions, err := service.TenantPermissionCeiling(ctx, "tenant-1")
	require.NoError(t, err)
	require.Equal(t, []PermissionView{{ID: "permission-read", PermissionKey: "tenant.member.read", Name: "查看成员", Resource: "tenant.member", Action: "read"}}, permissions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantPermissionCeilingRejectsTenantPrincipalBeforeDatabase(t *testing.T) {
	t.Parallel()
	service := &TenantAuthorizationService{}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	_, err := service.TenantPermissionCeiling(ctx, "tenant-1")
	require.ErrorIs(t, err, ErrTenantAuthorizationForbidden)
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
	mock.ExpectQuery(`SELECT id,resource,action FROM permissions`).WithArgs("permission-delete").WillReturnRows(sqlmock.NewRows([]string{"id", "resource", "action"}).AddRow("permission-delete", "tenant.member", "remove"))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id FROM tenant_role_permissions`).WithArgs("tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("permission-read"))

	actor := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"}
	service := &TenantAuthorizationService{registry: testAuthorizationRegistry(t)}
	require.ErrorIs(t, service.ensureRoleAssignable(t.Context(), tx, actor, "role-1"), ErrTenantAuthorizationForbidden)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
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
	mock.ExpectQuery(`SELECT id,resource,action FROM permissions`).WithArgs("permission-platform").WillReturnRows(
		sqlmock.NewRows([]string{"id", "resource", "action"}).AddRow("permission-platform", "identity.user", "read"),
	)
	service := &TenantAuthorizationService{registry: testAuthorizationRegistry(t)}
	require.ErrorIs(t, service.validateTenantPermissions(t.Context(), tx, []string{"permission-platform"}), ErrTenantAuthorizationInvalid)
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
		testAuthorizationRegistry(t),
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
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status`).
		WithArgs("tenant-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "code", "name", "description", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow("role-1", "tenant-1", "auditor", "Auditor", "", "active", now, "user-1", now, "user-1", 1))

	role, err := service.CreateRole(ctx, "auditor", "Auditor", "", nil)
	require.NoError(t, err)
	require.Equal(t, "role-1", role.ID)
	require.NoError(t, ctx.Err())
	require.NoError(t, mock.ExpectationsWereMet())
}

func testAuthorizationRegistry(t *testing.T) *pbac.Registry {
	t.Helper()
	registry, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	return registry
}
