package tenant

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

const departmentActionID = "15be7aa9-0b1a-42a6-9530-6b18a6dd4878"
const departmentParentID = "277ec751-2e2f-42f3-8753-3d4fd424d50f"

func TestDepartmentActionTargetsProjectOnlyMatchingFields(t *testing.T) {
	for _, name := range []string{"update", "delete"} {
		t.Run(name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			now := time.Now()
			mock.ExpectQuery(`SELECT .* FROM tenant_departments td WHERE td.tenant_id=\? AND td.id=\? AND td.deleted_at IS NULL AND \(1 = 1\)`).
				WithArgs("tenant-a", departmentActionID).
				WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}).
					AddRow(departmentActionID, "tenant-a", nil, "engineering", "Engineering", 10, now, "creator", now, "updater", 7))
			service := &DepartmentService{db: sqlx.NewDb(raw, "sqlmock")}
			ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
				ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-a", MembershipID: "member-a",
			})
			if name == "update" {
				target, err := service.UpdateTarget(ctx, departmentActionID)
				require.NoError(t, err)
				require.Equal(t, DepartmentUpdateTarget{
					ID: departmentActionID, Code: "engineering", Name: "Engineering", SortOrder: 10, Version: 7,
				}, target)
			} else {
				target, err := service.DeleteTarget(ctx, departmentActionID)
				require.NoError(t, err)
				require.Equal(t, DepartmentDeleteTarget{
					ID: departmentActionID, Code: "engineering", Name: "Engineering", Version: 7,
				}, target)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestDepartmentUpdateTargetIncludesVisibleParentName(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM tenant_departments td WHERE td.tenant_id=\? AND td.id=\? AND td.deleted_at IS NULL AND \(1 = 1\)`).
		WithArgs("tenant-a", departmentActionID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow(departmentActionID, "tenant-a", departmentParentID, "backend", "Backend", 20, now, "creator", now, "updater", 4))
	mock.ExpectQuery(`SELECT td.name FROM tenant_departments td WHERE td.tenant_id=\? AND td.id=\? AND td.deleted_at IS NULL AND \(1 = 1\)`).
		WithArgs("tenant-a", departmentParentID).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Engineering"))
	service := &DepartmentService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-a", MembershipID: "member-a",
	})
	target, err := service.UpdateTarget(ctx, departmentActionID)
	require.NoError(t, err)
	require.Equal(t, departmentParentID, *target.ParentID)
	require.Equal(t, "Engineering", target.ParentName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDepartmentActionTargetRejectsBadIDAndMissingScope(t *testing.T) {
	service := &DepartmentService{}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-a", Type: platformprincipal.TypeUser, TenantID: "tenant-a", MembershipID: "member-a",
	})
	_, err := service.UpdateTarget(ctx, "not-uuid")
	require.ErrorIs(t, err, ErrInvalid)
	_, err = service.DeleteTarget(ctx, departmentActionID)
	require.Error(t, err)
}

func TestDepartmentActionTargetHidesOtherTenantRows(t *testing.T) {
	for _, name := range []string{"update", "delete"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			mock.ExpectQuery(`SELECT .* FROM tenant_departments td WHERE td.tenant_id=\? AND td.id=\? AND td.deleted_at IS NULL AND \(1 = 1\)`).
				WithArgs("tenant-a", departmentActionID).
				WillReturnRows(sqlmock.NewRows([]string{"id"}))
			service := &DepartmentService{db: sqlx.NewDb(raw, "sqlmock")}
			ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
				ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-a", MembershipID: "member-a",
			})
			if name == "update" {
				_, err = service.UpdateTarget(ctx, departmentActionID)
			} else {
				_, err = service.DeleteTarget(ctx, departmentActionID)
			}
			require.ErrorIs(t, err, ErrNotFound)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
