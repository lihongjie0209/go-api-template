package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type failedActorResolver struct{}

func (failedActorResolver) ResolveUserIDs(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("identity unavailable")
}

func applicationRows(id string, version int64) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows([]string{"id", "code", "name", "description", "icon", "home_path", "status", "sort_order", "metadata", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow(id, "erp", "ERP", "", "", "/erp", "active", 0, []byte(`{}`), now, "creator", now, "updater", version)
}

func TestApplicationMutationsDoNotRequireActorDisplayService(t *testing.T) {
	t.Parallel()
	id, err := StableID("erp")
	require.NoError(t, err)
	for _, operation := range []string{"create", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			db := sqlx.NewDb(raw, "sqlmock")
			service := New(db, database.NewTransactor(db), nil, failedActorResolver{})
			if operation != "create" {
				mock.ExpectQuery(`SELECT .* FROM applications WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnRows(applicationRows(id, 4))
			}
			mock.ExpectBegin()
			switch operation {
			case "create":
				mock.ExpectExec(`INSERT INTO applications`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery(`SELECT .* FROM applications WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnRows(applicationRows(id, 1))
			case "update":
				mock.ExpectExec(`UPDATE applications SET name=`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery(`SELECT .* FROM applications WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnRows(applicationRows(id, 5))
			case "delete":
				mock.ExpectQuery(`SELECT count\(\*\) FROM navigations`).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
				mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_application_grants`).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
				mock.ExpectExec(`UPDATE applications SET deleted_at=`).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectCommit()
			ctx := platformprincipal.SystemContext(t.Context(), "admin")
			switch operation {
			case "create":
				result, err := service.Create(ctx, Input{Code: "erp", Name: "ERP", HomePath: "/erp", Status: "active"})
				require.NoError(t, err)
				require.Equal(t, id, result.ID)
				require.Equal(t, "creator", result.CreatedByName)
			case "update":
				result, err := service.Update(ctx, UpdateInput{ID: id, Name: "ERP", HomePath: "/erp", Status: "active", Metadata: []byte(`{}`), Version: 4})
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

func TestApplicationCreateRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	id, err := StableID("erp")
	require.NoError(t, err)
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := New(db, database.NewTransactor(db), nil, nil)
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO applications`).WillReturnResult(sqlmock.NewResult(0, 1))
	readFailure := errors.New("transactional result read failed")
	mock.ExpectQuery(`SELECT .* FROM applications WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnError(readFailure)
	mock.ExpectRollback()
	_, err = service.Create(platformprincipal.SystemContext(t.Context(), "admin"), Input{Code: "erp", Name: "ERP", HomePath: "/erp", Status: "active"})
	require.ErrorIs(t, err, readFailure)
	require.NoError(t, mock.ExpectationsWereMet())
}
