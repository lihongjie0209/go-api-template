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

func TestUpdateTargetUsesCurrentTenantAndFreshEditableQuery(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	query := regexp.QuoteMeta(`SELECT id, code, name, description, status, version FROM tenants WHERE id = $1 AND deleted_at IS NULL`)
	mock.ExpectQuery(query).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "description", "status", "version"}).AddRow("tenant-a", "demo", "Demo", "Description", "active", 3))
	service := &Service{repository: NewRepository(sqlx.NewDb(raw, "pgx"))}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "actor", Type: platformprincipal.TypeUser, TenantID: "tenant-a"})
	target, err := service.UpdateTarget(ctx)
	require.NoError(t, err)
	require.Equal(t, UpdateTarget{ID: "tenant-a", Code: "demo", Name: "Demo", Description: "Description", Status: StatusActive, Version: 3}, target)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateTargetRejectsMissingTenantAndPropagatesQueryFailures(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &Service{repository: NewRepository(sqlx.NewDb(raw, "pgx"))}
	_, err = service.UpdateTarget(t.Context())
	require.ErrorIs(t, err, platformprincipal.ErrMissing)
	_, err = service.UpdateTarget(platformprincipal.SystemContext(t.Context(), "system"))
	require.ErrorIs(t, err, ErrForbidden)
	query := regexp.QuoteMeta(`SELECT id, code, name, description, status, version FROM tenants WHERE id = $1 AND deleted_at IS NULL`)
	mock.ExpectQuery(query).WithArgs("tenant-b").WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "description", "status", "version"}))
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "actor", Type: platformprincipal.TypeUser, TenantID: "tenant-b"})
	_, err = service.UpdateTarget(ctx)
	require.ErrorIs(t, err, ErrNotFound)
	mock.ExpectQuery(query).WithArgs("tenant-b").WillReturnError(errors.New("database unavailable"))
	_, err = service.UpdateTarget(ctx)
	require.ErrorContains(t, err, "database unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}
