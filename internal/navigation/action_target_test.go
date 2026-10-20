package navigation

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestNavigationActionTargets(t *testing.T) {
	t.Parallel()
	const id = "63b3212e-e383-5f26-ad10-c4e30d778918"
	const appID = "bed5ee41-d555-5f37-b357-6f8f2314f096"
	const parentID = "d1979c08-1de5-5df3-b74b-77e848a6fc96"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &Service{db: sqlx.NewDb(raw, "pgx")}
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	mock.ExpectQuery(`SELECT n.id,n.application_id,a.name AS application_name,n.parent_id.*a.deleted_at IS NULL.*n.id=\$1 AND n.deleted_at IS NULL`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{
		"id", "application_id", "application_name", "parent_id", "parent_name", "navigation_key", "name", "navigation_type", "route_path", "component", "icon", "resource", "action", "visible", "status", "sort_order", "metadata", "version",
	}).AddRow(id, appID, "仓储应用", parentID, "系统", "inventory", "库存", "menu", "/inventory", "platform/inventory", "box", "warehouse.item", "read", true, "active", 10, []byte(`{}`), 4))
	update, err := service.UpdateTarget(ctx, strings.ToUpper(id))
	require.NoError(t, err)
	require.Equal(t, id, update.ID)
	require.Equal(t, "仓储应用", update.ApplicationName)
	require.Equal(t, "系统", update.ParentName)
	require.Equal(t, parentID, *update.ParentID)
	require.Equal(t, int64(4), update.Version)
	require.JSONEq(t, `{}`, string(update.Metadata))

	mock.ExpectQuery(`SELECT n.id,n.application_id,a.name AS application_name,n.navigation_key.*a.deleted_at IS NULL.*n.id=\$1 AND n.deleted_at IS NULL`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{
		"id", "application_id", "application_name", "navigation_key", "name", "navigation_type", "version",
	}).AddRow(id, appID, "仓储应用", "inventory", "库存", "menu", 4))
	deletion, err := service.DeleteTarget(ctx, id)
	require.NoError(t, err)
	require.Equal(t, DeleteTarget{ID: id, ApplicationID: appID, ApplicationName: "仓储应用", Key: "inventory", Name: "库存", Type: "menu", Version: 4}, deletion)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestNavigationActionTargetsRejectInvalidMissingOrUnauthenticated(t *testing.T) {
	t.Parallel()
	const id = "63b3212e-e383-5f26-ad10-c4e30d778918"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &Service{db: sqlx.NewDb(raw, "pgx")}
	_, err = service.UpdateTarget(t.Context(), id)
	require.Error(t, err)
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	_, err = service.DeleteTarget(ctx, "bad")
	require.ErrorIs(t, err, ErrInvalid)
	mock.ExpectQuery(`SELECT n.id,n.application_id,a.name AS application_name,n.navigation_key`).
		WithArgs(id).WillReturnError(sql.ErrNoRows)
	_, err = service.DeleteTarget(ctx, id)
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestNavigationMutationsRejectStaleVersionBeforeMutation(t *testing.T) {
	t.Parallel()
	const id = "63b3212e-e383-5f26-ad10-c4e30d778918"
	const appID = "bed5ee41-d555-5f37-b357-6f8f2314f096"
	for _, test := range []struct {
		name   string
		mutate func(*Service) error
	}{
		{name: "update", mutate: func(service *Service) error {
			_, err := service.Update(platformprincipal.SystemContext(t.Context(), "admin"), UpdateInput{ID: id, Name: "库存", Type: "directory", Visible: true, Status: "active", Version: 3})
			return err
		}},
		{name: "delete", mutate: func(service *Service) error {
			return service.Delete(platformprincipal.SystemContext(t.Context(), "admin"), id, 3)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			service := &Service{db: sqlx.NewDb(raw, "pgx")}
			now := time.Now()
			mock.ExpectQuery(`SELECT n.id,n.application_id,n.parent_id`).WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{
					"id", "application_id", "parent_id", "navigation_key", "name", "navigation_type", "route_path", "component", "icon", "resource", "action", "visible", "status", "sort_order", "metadata", "created_at", "created_by", "updated_at", "updated_by", "version", "parent_name",
				}).AddRow(id, appID, nil, "inventory", "库存", "directory", "", "", "", "", "", true, "active", 10, []byte(`{}`), now, "admin", now, "admin", 4, ""))
			require.ErrorIs(t, test.mutate(service), ErrStaleVersion)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
