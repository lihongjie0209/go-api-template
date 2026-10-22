package platformconfig

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/lihongjie0209/microservice-platform-go/stableid"
	"github.com/stretchr/testify/require"
)

type unavailableConfigActors struct{}

func (unavailableConfigActors) ResolveUserIDs(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("identity unavailable")
}

func configRows(id string, version int64) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows([]string{"id", "config_key", "name", "category", "value_type", "value", "description", "is_public", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow(id, "ui.theme", "Theme", "ui", "object", []byte(`{}`), "", true, "active", now, "creator", now, "updater", version)
}

func TestConfigMutationsDoNotRequireActorDisplayService(t *testing.T) {
	t.Parallel()
	generator, err := stableid.New(platformConfigNamespace)
	require.NoError(t, err)
	id, err := generator.String("platform-config:ui.theme")
	require.NoError(t, err)
	for _, operation := range []string{"create", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			db := sqlx.NewDb(raw, "sqlmock")
			service := &Service{db: db, tx: database.NewTransactor(db), actors: unavailableConfigActors{}}
			if operation != "create" {
				mock.ExpectQuery(`SELECT .* FROM platform_configs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnRows(configRows(id, 4))
			}
			mock.ExpectBegin()
			if operation == "create" {
				mock.ExpectQuery(`SELECT deleted_at FROM platform_configs WHERE id=\? FOR UPDATE`).WithArgs(id).WillReturnError(sql.ErrNoRows)
				mock.ExpectExec(`INSERT INTO platform_configs`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery(`SELECT .* FROM platform_configs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnRows(configRows(id, 1))
				mock.ExpectCommit()
			} else if operation == "update" {
				mock.ExpectExec(`UPDATE platform_configs SET name=`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery(`SELECT .* FROM platform_configs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnRows(configRows(id, 5))
				mock.ExpectCommit()
			} else {
				mock.ExpectExec(`UPDATE platform_configs SET deleted_at=`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			ctx := platformprincipal.SystemContext(t.Context(), "admin")
			switch operation {
			case "create":
				result, err := service.Create(ctx, Input{Key: "ui.theme", Name: "Theme", Category: "ui", Value: []byte(`{}`), IsPublic: true, Status: "active"})
				require.NoError(t, err)
				require.EqualValues(t, 1, result.Version)
				require.Equal(t, "creator", result.CreatedByName)
			case "update":
				result, err := service.Update(ctx, UpdateInput{ID: id, Name: "Theme", Category: "ui", Value: []byte(`{}`), IsPublic: true, Status: "active", Version: 4})
				require.NoError(t, err)
				require.EqualValues(t, 5, result.Version)
				require.Equal(t, "creator", result.CreatedByName)
			case "delete":
				require.NoError(t, service.Delete(ctx, id, 4))
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestConfigCreateRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	generator, err := stableid.New(platformConfigNamespace)
	require.NoError(t, err)
	id, err := generator.String("platform-config:ui.theme")
	require.NoError(t, err)
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := &Service{db: db, tx: database.NewTransactor(db)}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT deleted_at FROM platform_configs WHERE id=\? FOR UPDATE`).WithArgs(id).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO platform_configs`).WillReturnResult(sqlmock.NewResult(0, 1))
	readFailure := errors.New("transactional result read failed")
	mock.ExpectQuery(`SELECT .* FROM platform_configs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnError(readFailure)
	mock.ExpectRollback()
	_, err = service.Create(platformprincipal.SystemContext(t.Context(), "admin"), Input{Key: "ui.theme", Name: "Theme", Category: "ui", Value: []byte(`{}`), IsPublic: true, Status: "active"})
	require.ErrorIs(t, err, readFailure)
	require.NoError(t, mock.ExpectationsWereMet())
}
