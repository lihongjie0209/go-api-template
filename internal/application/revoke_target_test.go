package application

import (
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func grantTargetRows(status string) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "tenant_name", "application_id", "application_code", "application_name",
		"application_icon", "application_home_path", "status", "starts_at", "expires_at",
		"created_at", "created_by", "updated_at", "updated_by", "version",
	}).AddRow(
		"grant-1", "tenant-1", "测试租户", "app-1", "erp", "ERP 管理", "icon", "/erp", status,
		nil, nil, now, "admin", now, "admin", 3,
	)
}

func TestRevokeTargetReturnsScopedMinimalActiveGrant(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, raw.Close())
	})
	mock.ExpectQuery(`FROM tenant_application_grants g JOIN tenants t.*WHERE g.tenant_id=\? AND g.id=\? AND g.deleted_at IS NULL`).
		WithArgs("tenant-1", "grant-1").WillReturnRows(grantTargetRows("active"))
	service := NewTenantAccessService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil)
	target, err := service.RevokeTarget(platformprincipal.SystemContext(t.Context(), "admin"), "tenant-1", "grant-1")
	require.NoError(t, err)
	require.Equal(t, GrantRevokeTarget{
		ID: "grant-1", TenantID: "tenant-1", TenantName: "测试租户",
		ApplicationID: "app-1", ApplicationCode: "erp", ApplicationName: "ERP 管理",
		Status: "active", Version: 3,
	}, target)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRevokeTargetRejectsForbiddenMissingAndRevokedGrants(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, raw.Close())
	})
	service := NewTenantAccessService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil)
	_, err = service.RevokeTarget(t.Context(), "tenant-1", "grant-1")
	require.ErrorIs(t, err, ErrGrantForbidden)
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	_, err = service.RevokeTarget(ctx, "", "grant-1")
	require.ErrorIs(t, err, ErrGrantInvalid)
	query := `FROM tenant_application_grants g JOIN tenants t.*WHERE g.tenant_id=\? AND g.id=\? AND g.deleted_at IS NULL`
	mock.ExpectQuery(query).WithArgs("tenant-1", "grant-1").WillReturnError(sql.ErrNoRows)
	_, err = service.RevokeTarget(ctx, "tenant-1", "grant-1")
	require.ErrorIs(t, err, ErrGrantNotFound)
	mock.ExpectQuery(query).WithArgs("tenant-1", "grant-1").WillReturnRows(grantTargetRows("revoked"))
	_, err = service.RevokeTarget(ctx, "tenant-1", "grant-1")
	require.ErrorIs(t, err, ErrGrantConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}
