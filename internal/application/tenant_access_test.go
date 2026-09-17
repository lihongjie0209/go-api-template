package application

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestValidGrantInputRequiresOrderedValidityWindow(t *testing.T) {
	t.Parallel()
	start := time.Now()
	end := start.Add(time.Hour)
	require.True(t, validGrantInput(GrantInput{TenantID: "tenant-1", ApplicationID: "app-1", StartsAt: &start, ExpiresAt: &end}))
	require.False(t, validGrantInput(GrantInput{TenantID: "tenant-1", ApplicationID: "app-1", StartsAt: &end, ExpiresAt: &start}))
	require.False(t, validGrantInput(GrantInput{ApplicationID: "app-1"}))
}

func TestGrantOrderUsesAllowlist(t *testing.T) {
	t.Parallel()
	order, err := grantOrder([]pagination.Sort{{Field: "application_name", Direction: "asc"}})
	require.NoError(t, err)
	require.Equal(t, "a.name ASC,g.id ASC", order)
	_, err = grantOrder([]pagination.Sort{{Field: "g.id;drop", Direction: "asc"}})
	require.ErrorIs(t, err, ErrGrantInvalid)
}

func TestTenantAccessPlatformOperationsRejectTenantPrincipal(t *testing.T) {
	t.Parallel()
	service := &TenantAccessService{}
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	_, err := service.Grant(ctx, GrantInput{TenantID: "tenant-1", ApplicationID: "app-1"})
	require.ErrorIs(t, err, ErrGrantForbidden)
}

func TestCurrentApplicationsScopesTenantUserAndMembershipInSQL(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	now := time.Now()
	mock.ExpectQuery(`FROM tenant_application_grants g JOIN applications a.*JOIN tenant_memberships m.*WHERE g.tenant_id=\?`).
		WithArgs("membership-1", "user-1", "tenant-1", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "icon", "home_path", "sort_order", "starts_at", "expires_at"}).AddRow("app-1", "console", "Console", "", "/", 0, now, nil))
	service := &TenantAccessService{db: db}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "membership-1"})
	items, err := service.Current(ctx)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "Console", items[0].Name)
	_, offset := items[0].StartsAt.Zone()
	require.Equal(t, 8*60*60, offset)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCurrentApplicationsRejectsMissingTenantContext(t *testing.T) {
	t.Parallel()
	service := &TenantAccessService{}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	_, err := service.Current(ctx)
	require.ErrorIs(t, err, ErrGrantForbidden)
}
