package authorization

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

const roleActionID = "15be7aa9-0b1a-42a6-9530-6b18a6dd4878"

func TestRoleActionTargetsUseTenantScopedLookupAndNarrowProjection(t *testing.T) {
	tests := []struct {
		name string
		want any
	}{
		{
			name: "update",
			want: RoleUpdateTarget{ID: roleActionID, Code: "reviewer", Name: "Reviewer", Description: "Internal", Status: "active", Version: 7},
		},
		{
			name: "delete",
			want: RoleDeleteTarget{ID: roleActionID, Code: "reviewer", Name: "Reviewer", Status: "active", Version: 7},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			now := time.Now()
			mock.ExpectQuery(`SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status,tr.created_at,tr.created_by,tr.updated_at,tr.updated_by,tr.version FROM tenant_roles tr WHERE tr.tenant_id=\? AND tr.id=\? AND tr.deleted_at IS NULL AND \(1 = 1\)`).
				WithArgs("tenant-1", roleActionID).
				WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "code", "name", "description", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
					AddRow(roleActionID, "tenant-1", "reviewer", "Reviewer", "Internal", "active", now, "system", now, "system", 7))
			service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
			ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
				ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1",
			})
			var got any
			switch test.name {
			case "update":
				got, err = service.RoleUpdateTarget(ctx, roleActionID)
			case "delete":
				got, err = service.RoleDeleteTarget(ctx, roleActionID)
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestRoleActionTargetsRejectBadIDAndMissingScope(t *testing.T) {
	service := &TenantAuthorizationService{}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1",
	})
	_, err := service.RoleUpdateTarget(ctx, "bad-id")
	require.ErrorIs(t, err, ErrTenantAuthorizationInvalid)
	_, err = service.RoleDeleteTarget(ctx, roleActionID)
	require.Error(t, err)
}

func TestRoleActionTargetsHideAnotherTenantRole(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status,tr.created_at,tr.created_by,tr.updated_at,tr.updated_by,tr.version FROM tenant_roles tr WHERE tr.tenant_id=\? AND tr.id=\? AND tr.deleted_at IS NULL AND \(1 = 1\)`).
		WithArgs("tenant-1", roleActionID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	service := &TenantAuthorizationService{db: sqlx.NewDb(raw, "sqlmock")}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1", MembershipID: "member-1",
	})
	_, err = service.RoleDeleteTarget(ctx, roleActionID)
	require.ErrorIs(t, err, ErrTenantAuthorizationNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
