package authorization

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

const assignmentMemberID = "15be7aa9-0b1a-42a6-9530-6b18a6dd4878"

func TestMemberRoleAssignmentTargetReturnsTenantScopedSnapshot(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT tm.id,tm.username,tm.display_name,tm.status,tm.version FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.id=\? AND tm.deleted_at IS NULL AND \(1 = 1\)`).
		WithArgs("tenant-1", assignmentMemberID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "status", "version"}).
			AddRow(assignmentMemberID, "alice", "Alice", "active", 7))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships tm WHERE tm.tenant_id=`).
		WithArgs("tenant-1", assignmentMemberID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT r.id,r.code,r.name,r.status FROM tenant_member_roles mr`).
		WithArgs("tenant-1", assignmentMemberID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "status"}).
			AddRow("role-1", "reviewer", "Reviewer", "disabled"))
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "actor-1",
	})
	target, err := service.MemberRoleAssignmentTarget(ctx, assignmentMemberID)
	require.NoError(t, err)
	require.Equal(t, MemberRoleAssignmentTarget{
		ID: assignmentMemberID, Username: "alice", DisplayName: "Alice", Status: "active", Version: 7,
		Roles: []MemberRoleAssignmentView{{MemberRoleView: MemberRoleView{ID: "role-1", Code: "reviewer", Name: "Reviewer", Status: "disabled"}}},
	}, target)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMemberRoleAssignmentTargetMarksActiveRoleOutsideCallerCeiling(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT tm.id,tm.username`).WithArgs("tenant-1", assignmentMemberID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "status", "version"}).AddRow(assignmentMemberID, "alice", "Alice", "active", 7))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships tm`).WithArgs("tenant-1", assignmentMemberID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT r.id,r.code`).WithArgs("tenant-1", assignmentMemberID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "status"}).AddRow("role-1", "reader", "Reader", "active"))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "actor-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators`).WithArgs("tenant-1", "actor-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id`).WithArgs("tenant-1", "actor-1").WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("perm-read"))
	mock.ExpectQuery(`SELECT rp.role_id,rp.permission_id`).WithArgs("tenant-1", "role-1").WillReturnRows(sqlmock.NewRows([]string{"role_id", "permission_id", "valid"}).AddRow("role-1", "perm-write", 1))
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "actor-1"})
	target, err := service.MemberRoleAssignmentTarget(ctx, assignmentMemberID)
	require.NoError(t, err)
	require.False(t, target.Roles[0].Assignable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMemberRoleAssignmentTargetHidesForeignMemberAndFailsClosed(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	user := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "actor-1",
	})
	_, err = service.MemberRoleAssignmentTarget(user, assignmentMemberID)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	system := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "actor-1",
	})
	mock.ExpectQuery(`SELECT tm.id,tm.username,tm.display_name,tm.status,tm.version FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.id=\? AND tm.deleted_at IS NULL AND \(1 = 1\)`).
		WithArgs("tenant-1", assignmentMemberID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "status", "version"}))
	_, err = service.MemberRoleAssignmentTarget(system, assignmentMemberID)
	require.ErrorIs(t, err, ErrTenantAuthorizationNotFound)
	require.NoError(t, mock.ExpectationsWereMet())

	_, err = service.MemberRoleAssignmentTarget(system, "not-a-uuid")
	require.ErrorIs(t, err, ErrTenantAuthorizationInvalid)
	require.NoError(t, mock.ExpectationsWereMet())
}
