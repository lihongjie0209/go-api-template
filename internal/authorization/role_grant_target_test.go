package authorization

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestRoleGrantTargetReturnsTenantScopedSnapshot(t *testing.T) {
	t.Parallel()
	const roleID = "15be7aa9-0b1a-42a6-9530-6b18a6dd4878"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	now := time.Now()
	mock.ExpectQuery(`SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status,tr.created_at,tr.created_by,tr.updated_at,tr.updated_by,tr.version FROM tenant_roles tr WHERE tr.tenant_id=\? AND tr.id=\? AND tr.deleted_at IS NULL AND \(1 = 1\)`).
		WithArgs("tenant-1", roleID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "code", "name", "description", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow(roleID, "tenant-1", "reviewer", "Reviewer", "", "active", now, "system", now, "system", 7))
	mock.ExpectQuery(`SELECT p.id,p.permission_key,p.name,p.resource,p.action,p.status FROM tenant_role_permissions rp`).
		WithArgs("tenant-1", roleID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "permission_key", "name", "resource", "action", "status"}).
			AddRow("perm-1", "tenant.member.read", "Read members", "tenant.member", "read", "disabled"))
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1",
	})
	target, err := service.RoleGrantTarget(ctx, roleID)
	require.NoError(t, err)
	require.Equal(t, RoleGrantTarget{
		ID: roleID, Code: "reviewer", Name: "Reviewer", Status: "active", Version: 7,
		Permissions: []RoleGrantPermission{{PermissionView: PermissionView{ID: "perm-1", PermissionKey: "tenant.member.read", Name: "Read members", Resource: "tenant.member", Action: "read", Status: "disabled"}}},
	}, target)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRoleGrantTargetMarksActiveGrantOutsideCallerCeiling(t *testing.T) {
	const roleID = "15be7aa9-0b1a-42a6-9530-6b18a6dd4878"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	now := time.Now()
	mock.ExpectQuery(`SELECT tr.id,tr.tenant_id`).WithArgs("tenant-1", roleID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "code", "name", "description", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow(roleID, "tenant-1", "reviewer", "Reviewer", "", "active", now, "system", now, "system", 7))
	mock.ExpectQuery(`SELECT p.id,p.permission_key`).WithArgs("tenant-1", roleID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "permission_key", "name", "resource", "action", "status"}).
			AddRow("perm-read", "tenant.member.read", "Read", "tenant.member", "read", "active").
			AddRow("perm-write", "tenant.member.update", "Write", "tenant.member", "update", "active"))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "actor-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).WithArgs("tenant-1", "actor-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id`).WithArgs("tenant-1", "actor-1").WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("perm-read"))
	mock.ExpectQuery(`SELECT p.id FROM permissions p`).WithArgs("perm-read", "perm-write", "tenant-1").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("perm-read").AddRow("perm-write"))
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "actor-1"})
	target, err := service.RoleGrantTarget(ctx, roleID)
	require.NoError(t, err)
	require.True(t, target.Permissions[0].Assignable)
	require.False(t, target.Permissions[1].Assignable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRoleGrantTargetFailsClosedBeforeReadingLinks(t *testing.T) {
	t.Parallel()
	const roleID = "15be7aa9-0b1a-42a6-9530-6b18a6dd4878"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1",
	})
	_, err = service.RoleGrantTarget(ctx, roleID)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	_, err = service.RoleGrantTarget(ctx, "invalid")
	require.ErrorIs(t, err, ErrTenantAuthorizationInvalid)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRoleGrantTargetDoesNotReadLinksForAnotherTenantsRole(t *testing.T) {
	t.Parallel()
	const roleID = "15be7aa9-0b1a-42a6-9530-6b18a6dd4878"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status,tr.created_at,tr.created_by,tr.updated_at,tr.updated_by,tr.version FROM tenant_roles tr WHERE tr.tenant_id=\? AND tr.id=\? AND tr.deleted_at IS NULL AND \(1 = 1\)`).
		WithArgs("tenant-1", roleID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "code", "name", "description", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}))
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1",
	})
	_, err = service.RoleGrantTarget(ctx, roleID)
	require.ErrorIs(t, err, ErrTenantAuthorizationNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
