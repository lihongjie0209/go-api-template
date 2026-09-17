package tenant

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

func TestMemberCapabilityProviderLoadsTrustedAttributes(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	provider := NewMemberCapabilityProvider(sqlx.NewDb(raw, "sqlmock"))
	mock.ExpectQuery(`SELECT id,user_id,status,created_by FROM tenant_memberships WHERE tenant_id=\? AND id IN \(\?, \?\) AND deleted_at IS NULL`).
		WithArgs("tenant-1", "member-1", "missing-member").
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "status", "created_by"}).AddRow("member-1", "user-1", "active", "admin-1"))

	rows, err := provider.Load(t.Context(), "tenant-1", []string{"member-1", "missing-member"})
	require.NoError(t, err)
	require.Equal(t, "user-1", rows["member-1"]["owner_id"])
	require.NotContains(t, rows, "missing-member")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDepartmentCapabilityProviderNormalizesRootParent(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	provider := NewDepartmentCapabilityProvider(sqlx.NewDb(raw, "sqlmock"))
	mock.ExpectQuery(`SELECT id,parent_id,code,name,created_by FROM tenant_departments WHERE tenant_id=\? AND id IN \(\?\) AND deleted_at IS NULL`).
		WithArgs("tenant-1", "department-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "parent_id", "code", "name", "created_by"}).AddRow("department-1", nil, "root", "Root", "admin-1"))

	rows, err := provider.Load(t.Context(), "tenant-1", []string{"department-1"})
	require.NoError(t, err)
	require.Equal(t, "", rows["department-1"]["parent_id"])
	require.NoError(t, mock.ExpectationsWereMet())
}
