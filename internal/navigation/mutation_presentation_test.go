package navigation

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type failedNavigationActors struct{}

func (failedNavigationActors) ResolveUserIDs(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("identity unavailable")
}

func navigationDetailRows(id string, version int64) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows([]string{"id", "application_id", "parent_id", "navigation_key", "name", "navigation_type", "route_path", "component", "icon", "resource", "action", "visible", "status", "sort_order", "metadata", "created_at", "created_by", "updated_at", "updated_by", "version", "parent_name"}).
		AddRow(id, "app-1", nil, "root", "Root", "directory", "/root", "", "", "", "", true, "active", 0, []byte(`{}`), now, "creator", now, "updater", version, "")
}

func navigationTreeRows(id string) *sqlmock.Rows {
	now := time.Now()
	rows := sqlmock.NewRows([]string{"id", "application_id", "parent_id", "navigation_key", "name", "navigation_type", "route_path", "component", "icon", "resource", "action", "visible", "status", "sort_order", "metadata", "created_at", "created_by", "updated_at", "updated_by", "version"})
	if id != "" {
		rows.AddRow(id, "app-1", nil, "root", "Root", "directory", "/root", "", "", "", "", true, "active", 0, []byte(`{}`), now, "creator", now, "updater", 4)
	}
	return rows
}

func TestNavigationMutationsDoNotRequireActorDisplayService(t *testing.T) {
	t.Parallel()
	id, err := StableID("erp", "root")
	require.NoError(t, err)
	for _, operation := range []string{"create", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			db := sqlx.NewDb(raw, "sqlmock")
			service := &Service{db: db, tx: database.NewTransactor(db), actors: failedNavigationActors{}, logger: slog.Default()}
			switch operation {
			case "create":
				mock.ExpectQuery(`SELECT code FROM applications WHERE id=\?`).WithArgs("app-1").WillReturnRows(sqlmock.NewRows([]string{"code"}).AddRow("erp"))
			default:
				mock.ExpectQuery(`SELECT .* FROM navigations n LEFT JOIN navigations parent`).WithArgs(id).WillReturnRows(navigationDetailRows(id, 4))
			}
			mock.ExpectBegin()
			switch operation {
			case "create":
				mock.ExpectQuery(`SELECT code FROM applications WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).WithArgs("app-1").WillReturnRows(sqlmock.NewRows([]string{"code"}).AddRow("erp"))
				mock.ExpectQuery(`SELECT .* FROM navigations WHERE application_id=\?`).WithArgs("app-1", maxTreeNodes+1).WillReturnRows(navigationTreeRows(""))
				mock.ExpectExec(`INSERT INTO navigations`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery(`SELECT .* FROM navigations n LEFT JOIN navigations parent`).WithArgs(id).WillReturnRows(navigationDetailRows(id, 1))
			case "update":
				mock.ExpectQuery(`SELECT .* FROM navigations WHERE application_id=\?`).WithArgs("app-1", maxTreeNodes+1).WillReturnRows(navigationTreeRows(id))
				mock.ExpectExec(`UPDATE navigations SET parent_id=`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery(`SELECT .* FROM navigations n LEFT JOIN navigations parent`).WithArgs(id).WillReturnRows(navigationDetailRows(id, 5))
			case "delete":
				mock.ExpectQuery(`SELECT count\(\*\) FROM navigations WHERE parent_id=\?`).WithArgs(id, "app-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
				mock.ExpectExec(`UPDATE navigations SET deleted_at=`).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectCommit()
			ctx := platformprincipal.SystemContext(t.Context(), "admin")
			switch operation {
			case "create":
				result, err := service.Create(ctx, Input{ApplicationID: "app-1", Key: "root", Name: "Root", Type: "directory", RoutePath: "/root", Visible: true, Status: "active"})
				require.NoError(t, err)
				require.Equal(t, id, result.ID)
				require.Equal(t, "creator", result.CreatedByName)
			case "update":
				result, err := service.Update(ctx, UpdateInput{ID: id, Name: "Root", Type: "directory", RoutePath: "/root", Visible: true, Status: "active", Version: 4})
				require.NoError(t, err)
				require.EqualValues(t, 5, result.Version)
				require.Equal(t, "updater", result.UpdatedByName)
			case "delete":
				require.NoError(t, service.Delete(ctx, id, 4))
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestNavigationCreateRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	id, err := StableID("erp", "root")
	require.NoError(t, err)
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := &Service{db: db, tx: database.NewTransactor(db)}
	mock.ExpectQuery(`SELECT code FROM applications WHERE id=\?`).WithArgs("app-1").WillReturnRows(sqlmock.NewRows([]string{"code"}).AddRow("erp"))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT code FROM applications WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).WithArgs("app-1").WillReturnRows(sqlmock.NewRows([]string{"code"}).AddRow("erp"))
	mock.ExpectQuery(`SELECT .* FROM navigations WHERE application_id=\?`).WithArgs("app-1", maxTreeNodes+1).WillReturnRows(navigationTreeRows(""))
	mock.ExpectExec(`INSERT INTO navigations`).WillReturnResult(sqlmock.NewResult(0, 1))
	readFailure := errors.New("transactional result read failed")
	mock.ExpectQuery(`SELECT .* FROM navigations n LEFT JOIN navigations parent`).WithArgs(id).WillReturnError(readFailure)
	mock.ExpectRollback()
	_, err = service.Create(platformprincipal.SystemContext(t.Context(), "admin"), Input{ApplicationID: "app-1", Key: "root", Name: "Root", Type: "directory", RoutePath: "/root", Visible: true, Status: "active"})
	require.ErrorIs(t, err, readFailure)
	require.NoError(t, mock.ExpectationsWereMet())
}
