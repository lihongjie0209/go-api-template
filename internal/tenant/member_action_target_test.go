package tenant

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

const memberActionID = "15be7aa9-0b1a-42a6-9530-6b18a6dd4878"

func TestMemberActionTargetProjectsOnlyConfirmationFields(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &MembershipService{repository: NewRepository(sqlx.NewDb(raw, "sqlmock"))}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1",
	})
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.id=\? AND tm.deleted_at IS NULL AND \(1 = 1\)`).
		WithArgs("tenant-1", memberActionID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "user_id", "username", "display_name", "status", "joined_at",
			"created_at", "created_by", "updated_at", "updated_by", "version",
		}).AddRow(memberActionID, "tenant-1", "user-1", "alice", "Alice", "active", now, now, "creator", now, "updater", 7))

	target, err := service.ActionTarget(ctx, memberActionID)
	require.NoError(t, err)
	require.Equal(t, MemberActionTarget{
		ID: memberActionID, Username: "alice", DisplayName: "Alice", Status: StatusActive, Version: 7, IsSelf: false,
	}, target)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMemberActionTargetRejectsInvalidAndMissingTenant(t *testing.T) {
	service := &MembershipService{}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1",
	})
	_, err := service.ActionTarget(ctx, "not-a-uuid")
	require.ErrorIs(t, err, ErrInvalid)
	withoutTenant := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "system", Type: platformprincipal.TypeSystem,
	})
	_, err = service.ActionTarget(withoutTenant, memberActionID)
	require.ErrorIs(t, err, ErrForbidden)
}

func TestMemberActionTargetHidesRowsOutsideTenantScope(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &MembershipService{repository: NewRepository(sqlx.NewDb(raw, "sqlmock"))}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-1",
	})
	mock.ExpectQuery(`SELECT .* FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.id=\? AND tm.deleted_at IS NULL AND \(1 = 1\)`).
		WithArgs("tenant-1", memberActionID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	_, err = service.ActionTarget(ctx, memberActionID)
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
