package authorization

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

func TestRowCapabilityRegistryRejectsDuplicateResources(t *testing.T) {
	t.Parallel()
	provider := staticRowCapabilityProvider{resource: "tenant.member"}
	_, err := NewRowCapabilityRegistry([]RowCapabilityProvider{provider, provider})
	require.ErrorIs(t, err, ErrRowCapabilityProviderDuplicate)
}

func TestTenantRoleCapabilityProviderLoadsTenantScopedRows(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	provider := NewTenantRoleCapabilityProvider(sqlx.NewDb(raw, "sqlmock"))
	mock.ExpectQuery(`SELECT id,code,name,status,created_by FROM tenant_roles WHERE tenant_id=\? AND id IN \(\?, \?\) AND deleted_at IS NULL`).
		WithArgs("tenant-1", "role-1", "missing-role").
		WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "status", "created_by"}).AddRow("role-1", "manager", "Manager", "active", "user-1"))

	rows, err := provider.Load(t.Context(), "tenant-1", []string{"role-1", "missing-role"})
	require.NoError(t, err)
	require.Equal(t, "manager", rows["role-1"]["code"])
	require.NotContains(t, rows, "missing-role")
	require.NoError(t, mock.ExpectationsWereMet())
}
