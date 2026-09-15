package authorization

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
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
