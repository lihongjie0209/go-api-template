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

func TestAdminUpdateTargetRequiresPlatformPrincipalAndReadsNarrowFreshFields(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &Service{repository: NewRepository(sqlx.NewDb(raw, "pgx"))}
	_, err = service.AdminUpdateTarget(t.Context(), "tenant-a")
	require.ErrorIs(t, err, platformprincipal.ErrMissing)
	tenantCtx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "actor", Type: platformprincipal.TypeUser, TenantID: "tenant-a"})
	_, err = service.AdminUpdateTarget(tenantCtx, "tenant-a")
	require.ErrorIs(t, err, ErrForbidden)
	platformCtx := platformprincipal.SystemContext(t.Context(), "platform-admin")
	for _, id := range []string{"", " ", string(make([]byte, maxTenantIDLength+1))} {
		_, err = service.AdminUpdateTarget(platformCtx, id)
		require.ErrorIs(t, err, ErrInvalid)
	}
	query := regexp.QuoteMeta(`SELECT id, code, name, description, status, version FROM tenants WHERE id = $1 AND deleted_at IS NULL`)
	mock.ExpectQuery(query).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "description", "status", "version"}).AddRow("tenant-a", "demo", "Demo", "Description", "active", 3))
	target, err := service.AdminUpdateTarget(platformCtx, " tenant-a ")
	require.NoError(t, err)
	require.Equal(t, UpdateTarget{ID: "tenant-a", Code: "demo", Name: "Demo", Description: "Description", Status: StatusActive, Version: 3}, target)
	mock.ExpectQuery(query).WithArgs("tenant-b").WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "description", "status", "version"}))
	_, err = service.AdminUpdateTarget(platformCtx, "tenant-b")
	require.ErrorIs(t, err, ErrNotFound)
	mock.ExpectQuery(query).WithArgs("tenant-c").WillReturnError(errors.New("database unavailable"))
	_, err = service.AdminUpdateTarget(platformCtx, "tenant-c")
	require.ErrorContains(t, err, "database unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}
