package resourcecatalog

import (
	"database/sql"
	"database/sql/driver"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestCatalogActionTargetsOnlyAllowManualDefinitions(t *testing.T) {
	t.Parallel()
	resourceID := "6020111e-8d91-47e1-a272-2c81837a2542"
	actionID := "8c5a5b98-0d22-4260-944c-f44b92d941df"
	now := time.Now()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := NewService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil)
	resourceRows := func(source string) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"id", "resource_key", "name", "description", "scope", "source", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow(resourceID, "warehouse.item", "库存物料", "业务资源", "tenant", source, "active", now, "admin", now, "admin", 4)
	}
	actionRows := func(source string) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"id", "resource_id", "resource_key", "resource_name", "action_key", "name", "description", "source", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow(actionID, resourceID, "warehouse.item", "库存物料", "adjust", "调整库存", "业务动作", source, "active", now, "admin", now, "admin", 3)
	}
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	for _, source := range []string{"manual", "code"} {
		mock.ExpectQuery(`SELECT id,resource_key,name,description,scope,source,status,created_at,created_by,updated_at,updated_by,version FROM authorization_resources`).WithArgs(resourceID).WillReturnRows(resourceRows(source))
		update, err := service.ResourceUpdateTarget(ctx, resourceID)
		if source == "code" {
			require.ErrorIs(t, err, ErrCodeOwned)
		} else {
			require.NoError(t, err)
			require.Equal(t, ResourceUpdateTarget{ID: resourceID, Key: "warehouse.item", Name: "库存物料", Description: "业务资源", Scope: resourceaction.ResourceScopeTenant, Status: "active", Version: 4}, update)
		}
		mock.ExpectQuery(`SELECT id,resource_key,name,description,scope,source,status,created_at,created_by,updated_at,updated_by,version FROM authorization_resources`).WithArgs(resourceID).WillReturnRows(resourceRows(source))
		deletion, err := service.ResourceDeleteTarget(ctx, resourceID)
		if source == "code" {
			require.ErrorIs(t, err, ErrCodeOwned)
		} else {
			require.NoError(t, err)
			require.Equal(t, ResourceDeleteTarget{ID: resourceID, Key: "warehouse.item", Name: "库存物料", Status: "active", Version: 4}, deletion)
		}
		mock.ExpectQuery(`SELECT a.id,a.resource_id,r.resource_key,r.name AS resource_name`).WithArgs(actionID).WillReturnRows(actionRows(source))
		actionUpdate, err := service.ActionUpdateTarget(ctx, actionID)
		if source == "code" {
			require.ErrorIs(t, err, ErrCodeOwned)
		} else {
			require.NoError(t, err)
			require.Equal(t, ActionUpdateTarget{ID: actionID, ResourceID: resourceID, ResourceKey: "warehouse.item", ResourceName: "库存物料", Key: "adjust", Name: "调整库存", Description: "业务动作", Status: "active", Version: 3}, actionUpdate)
		}
		mock.ExpectQuery(`SELECT a.id,a.resource_id,r.resource_key,r.name AS resource_name`).WithArgs(actionID).WillReturnRows(actionRows(source))
		actionDelete, err := service.ActionDeleteTarget(ctx, actionID)
		if source == "code" {
			require.ErrorIs(t, err, ErrCodeOwned)
		} else {
			require.NoError(t, err)
			require.Equal(t, ActionDeleteTarget{ID: actionID, ResourceKey: "warehouse.item", ResourceName: "库存物料", Key: "adjust", Name: "调整库存", Status: "active", Version: 3}, actionDelete)
		}
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCatalogActionTargetsRejectUnauthenticatedInvalidAndMissing(t *testing.T) {
	t.Parallel()
	id := "6020111e-8d91-47e1-a272-2c81837a2542"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := NewService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil)
	_, err = service.ResourceUpdateTarget(t.Context(), id)
	require.Error(t, err)
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	_, err = service.ResourceDeleteTarget(ctx, "bad")
	require.ErrorIs(t, err, ErrInvalid)
	mock.ExpectQuery(`SELECT a.id,a.resource_id,r.resource_key,r.name AS resource_name`).WithArgs(id).WillReturnError(sql.ErrNoRows)
	_, err = service.ActionUpdateTarget(ctx, id)
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCatalogMutationsRejectStaleVersions(t *testing.T) {
	id := "6020111e-8d91-47e1-a272-2c81837a2542"
	now := time.Now()
	for _, test := range []struct {
		name    string
		query   string
		columns []string
		values  []driver.Value
		mutate  func(*Service, string) error
	}{
		{
			name:    "resource update",
			query:   `SELECT id,resource_key,name,description,scope,source,status`,
			columns: []string{"id", "resource_key", "name", "description", "scope", "source", "status", "created_at", "created_by", "updated_at", "updated_by", "version"},
			values:  []driver.Value{id, "warehouse.item", "库存物料", "", "tenant", "manual", "active", now, "admin", now, "admin", 8},
			mutate: func(service *Service, id string) error {
				_, err := service.UpdateResource(platformprincipal.SystemContext(t.Context(), "admin"), id, 7, ResourceInput{Key: "warehouse.item", Name: "库存物料", Scope: resourceaction.ResourceScopeTenant, Status: "active"})
				return err
			},
		},
		{
			name:    "action delete",
			query:   `SELECT a.id,a.resource_id,r.resource_key,r.name AS resource_name`,
			columns: []string{"id", "resource_id", "resource_key", "resource_name", "action_key", "name", "description", "source", "status", "created_at", "created_by", "updated_at", "updated_by", "version"},
			values:  []driver.Value{id, id, "warehouse.item", "库存物料", "adjust", "调整库存", "", "manual", "active", now, "admin", now, "admin", 8},
			mutate: func(service *Service, id string) error {
				return service.DeleteAction(platformprincipal.SystemContext(t.Context(), "admin"), id, 7)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			db := sqlx.NewDb(raw, "pgx")
			service := NewService(db, database.NewTransactor(db), nil, nil)
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.actor_id', $1, true)")).WithArgs("admin").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(test.query).WithArgs(id).WillReturnRows(sqlmock.NewRows(test.columns).AddRow(test.values...))
			mock.ExpectRollback()
			err = test.mutate(service, id)
			require.ErrorIs(t, err, ErrStaleVersion)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
