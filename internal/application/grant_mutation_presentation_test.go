package application

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestGrantCommittedResponseDoesNotRequireActorDisplayService(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := NewTenantAccessService(db, database.NewTransactor(db), nil, nil, failedActorResolver{})
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT name FROM tenants WHERE id=\?`).WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Tenant"))
	mock.ExpectQuery(`SELECT name FROM applications WHERE id=\?`).WithArgs("app-1").WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("ERP"))
	mock.ExpectQuery(`SELECT id FROM tenant_application_grants WHERE tenant_id=\?`).WithArgs("tenant-1", "app-1").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("grant-1"))
	mock.ExpectExec(`UPDATE tenant_application_grants SET status='active'`).WithArgs(nil, nil, sqlmock.AnyArg(), "admin", "tenant-1", "app-1", int64(4)).WillReturnResult(sqlmock.NewResult(0, 1))
	now := time.Now()
	rows := sqlmock.NewRows([]string{"id", "tenant_id", "tenant_name", "application_id", "application_code", "application_name", "application_icon", "application_home_path", "status", "starts_at", "expires_at", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow("grant-1", "tenant-1", "Tenant", "app-1", "erp", "ERP", "", "/erp", "active", nil, nil, now, "creator", now, "updater", 5)
	mock.ExpectQuery(`SELECT .* FROM tenant_application_grants g JOIN tenants t`).WithArgs("tenant-1", "grant-1").WillReturnRows(rows)
	mock.ExpectCommit()
	result, err := service.Grant(platformprincipal.SystemContext(t.Context(), "admin"), GrantInput{TenantID: "tenant-1", ApplicationID: "app-1", Version: 4})
	require.NoError(t, err)
	require.EqualValues(t, 5, result.Version)
	require.Equal(t, "Tenant", result.TenantName)
	require.Equal(t, "creator", result.CreatedByName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGrantRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := NewTenantAccessService(db, database.NewTransactor(db), nil, nil, nil)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT name FROM tenants WHERE id=\?`).WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Tenant"))
	mock.ExpectQuery(`SELECT name FROM applications WHERE id=\?`).WithArgs("app-1").WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("ERP"))
	mock.ExpectQuery(`SELECT id FROM tenant_application_grants WHERE tenant_id=\?`).WithArgs("tenant-1", "app-1").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("grant-1"))
	mock.ExpectExec(`UPDATE tenant_application_grants SET status='active'`).WillReturnResult(sqlmock.NewResult(0, 1))
	readFailure := errors.New("transactional result read failed")
	mock.ExpectQuery(`SELECT .* FROM tenant_application_grants g JOIN tenants t`).WithArgs("tenant-1", "grant-1").WillReturnError(readFailure)
	mock.ExpectRollback()
	_, err = service.Grant(platformprincipal.SystemContext(t.Context(), "admin"), GrantInput{TenantID: "tenant-1", ApplicationID: "app-1", Version: 4})
	require.ErrorIs(t, err, readFailure)
	require.NoError(t, mock.ExpectationsWereMet())
}
