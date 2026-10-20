package tenant

import (
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestDeleteTargetUsesCurrentTenantAndFreshMinimalQuery(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	query := regexp.QuoteMeta(`SELECT id, code, name, status, version FROM tenants WHERE id = $1 AND deleted_at IS NULL`)
	mock.ExpectQuery(query).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "status", "version"}).AddRow("tenant-a", "demo", "Demo", "active", 3))
	service := &Service{repository: NewRepository(sqlx.NewDb(raw, "pgx"))}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "actor", Type: platformprincipal.TypeUser, TenantID: "tenant-a"})
	target, err := service.DeleteTarget(ctx)
	require.NoError(t, err)
	require.Equal(t, DeleteTarget{ID: "tenant-a", Code: "demo", Name: "Demo", Status: StatusActive, Version: 3}, target)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDeleteTargetRejectsMissingTenantAndMissingRow(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &Service{repository: NewRepository(sqlx.NewDb(raw, "pgx"))}
	_, err = service.DeleteTarget(t.Context())
	require.ErrorIs(t, err, platformprincipal.ErrMissing)
	_, err = service.DeleteTarget(platformprincipal.SystemContext(t.Context(), "system"))
	require.ErrorIs(t, err, ErrForbidden)
	query := regexp.QuoteMeta(`SELECT id, code, name, status, version FROM tenants WHERE id = $1 AND deleted_at IS NULL`)
	mock.ExpectQuery(query).WithArgs("tenant-b").WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "status", "version"}))
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "actor", Type: platformprincipal.TypeUser, TenantID: "tenant-b"})
	_, err = service.DeleteTarget(ctx)
	require.True(t, errors.Is(err, ErrNotFound))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAdminDeleteTargetRequiresPlatformPrincipalAndFreshRow(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &Service{repository: NewRepository(sqlx.NewDb(raw, "pgx"))}
	_, err = service.AdminDeleteTarget(t.Context(), "tenant-a")
	require.ErrorIs(t, err, platformprincipal.ErrMissing)
	tenantCtx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeUser, TenantID: "tenant-a"})
	_, err = service.AdminDeleteTarget(tenantCtx, "tenant-b")
	require.ErrorIs(t, err, ErrForbidden)
	platformCtx := platformprincipal.SystemContext(t.Context(), "platform-admin")
	for _, id := range []string{"", " ", string(make([]byte, maxTenantIDLength+1))} {
		_, err = service.AdminDeleteTarget(platformCtx, id)
		require.ErrorIs(t, err, ErrInvalid)
	}
	query := regexp.QuoteMeta(`SELECT id, code, name, status, version FROM tenants WHERE id = $1 AND deleted_at IS NULL`)
	mock.ExpectQuery(query).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "status", "version"}).AddRow("tenant-a", "demo", "Demo", "active", 8))
	target, err := service.AdminDeleteTarget(platformCtx, " tenant-a ")
	require.NoError(t, err)
	require.Equal(t, DeleteTarget{ID: "tenant-a", Code: "demo", Name: "Demo", Status: StatusActive, Version: 8}, target)
	mock.ExpectQuery(query).WithArgs("tenant-b").WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "status", "version"}))
	_, err = service.AdminDeleteTarget(platformCtx, "tenant-b")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
