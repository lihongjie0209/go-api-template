package tenant

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestContextService_AvailableScopesMembershipsToAuthenticatedUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	service := &ContextService{db: sqlxDB}
	mock.ExpectQuery(`FROM tenant_memberships m JOIN tenants`).WithArgs("user-1").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "tenant_code", "tenant_name", "membership_id", "is_administrator", "joined_at"}))
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	items, err := service.Available(ctx)
	require.NoError(t, err)
	require.NotNil(t, items)
	require.Empty(t, items)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContextService_CurrentRequiresActiveTenantAndMembership(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service := &ContextService{db: sqlx.NewDb(db, "sqlmock")}
	mock.ExpectQuery(`JOIN tenants t ON t.id=m.tenant_id AND t.status='active'.*WHERE m.tenant_id=\? AND m.id=\? AND m.user_id=\? AND m.status='active'`).WithArgs("tenant-1", "membership-1", "user-1").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "tenant_code", "tenant_name", "membership_id", "is_administrator", "joined_at"}))
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "membership-1"})
	_, err = service.Current(ctx)
	require.ErrorIs(t, err, ErrForbidden)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContextService_AvailableRejectsServiceAccount(t *testing.T) {
	service := &ContextService{}
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "service-1", Type: platformprincipal.TypeServiceAccount})
	_, err := service.Available(ctx)
	require.ErrorIs(t, err, ErrForbidden)
}
