package dictionary

import (
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestDefinitionActionTargetsProjectOnlyNeededFields(t *testing.T) {
	t.Parallel()
	id := uuidString(1)
	now := time.Now()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &Service{db: sqlx.NewDb(raw, "sqlmock")}
	rows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"id", "dictionary_code", "name", "dictionary_type", "source_type", "description", "status", "extension", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow(id, "region", "地区", "tree", "static", "地理区域", "active", []byte(`{"owner":"platform"}`), now, "admin", now, "admin", 4)
	}
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	mock.ExpectQuery(`SELECT id,dictionary_code,name,dictionary_type,source_type`).WithArgs(id).WillReturnRows(rows())
	update, err := service.UpdateTarget(ctx, id)
	require.NoError(t, err)
	require.Equal(t, UpdateTarget{ID: id, Code: "region", Name: "地区", Kind: KindTree, Source: SourceStatic, Description: "地理区域", Status: "active", Extension: []byte(`{"owner":"platform"}`), Version: 4}, update)
	mock.ExpectQuery(`SELECT id,dictionary_code,name,dictionary_type,source_type`).WithArgs(id).WillReturnRows(rows())
	deletion, err := service.DeleteTarget(ctx, id)
	require.NoError(t, err)
	require.Equal(t, DeleteTarget{ID: id, Code: "region", Name: "地区", Kind: KindTree, Source: SourceStatic, Status: "active", Version: 4}, deletion)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDefinitionActionTargetsRejectInvalidMissingAndUnauthenticated(t *testing.T) {
	t.Parallel()
	id := uuidString(1)
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &Service{db: sqlx.NewDb(raw, "sqlmock")}
	_, err = service.UpdateTarget(t.Context(), id)
	require.Error(t, err)
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	_, err = service.DeleteTarget(ctx, "bad")
	require.ErrorIs(t, err, ErrInvalid)
	mock.ExpectQuery(`SELECT id,dictionary_code,name,dictionary_type,source_type`).WithArgs(id).WillReturnError(sql.ErrNoRows)
	_, err = service.UpdateTarget(ctx, id)
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDefinitionMutationsRejectStaleVersions(t *testing.T) {
	t.Parallel()
	id := uuidString(1)
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	for _, test := range []struct {
		name  string
		setup func(sqlmock.Sqlmock)
		call  func(*Service) error
	}{
		{name: "update", setup: func(mock sqlmock.Sqlmock) {
			mock.ExpectExec(`UPDATE dictionary_definitions SET name=`).WillReturnResult(sqlmock.NewResult(0, 0))
		}, call: func(service *Service) error {
			_, err := service.UpdateDefinition(ctx, DefinitionUpdate{ID: id, Name: "地区", Status: "active", Version: 4})
			return err
		}},
		{name: "delete", setup: func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(`SELECT version FROM dictionary_definitions`).WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(5))
		}, call: func(service *Service) error {
			return service.DeleteDefinition(ctx, id, 4)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			db := sqlx.NewDb(raw, "sqlmock")
			service := &Service{db: db, tx: database.NewTransactor(db)}
			mock.ExpectBegin()
			test.setup(mock)
			mock.ExpectRollback()
			require.ErrorIs(t, test.call(service), ErrStaleVersion)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
