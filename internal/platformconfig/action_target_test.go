package platformconfig

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestActionTargetsReadNarrowProjections(t *testing.T) {
	t.Parallel()
	id := "20f40b91-76f1-5d16-bade-fc464610cbde"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &Service{db: sqlx.NewDb(raw, "sqlmock")}
	mock.ExpectQuery(`SELECT id,config_key,name,category,value_type,value,description,is_public,status,version FROM platform_configs WHERE id=\? AND deleted_at IS NULL`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"id", "config_key", "name", "category", "value_type", "value", "description", "is_public", "status", "version"}).
			AddRow(id, "ui.theme", "Theme", "ui", "object", []byte(`{"color":"blue"}`), "Display theme", true, "active", 4))
	mock.ExpectQuery(`SELECT id,config_key,name,is_public,status,version FROM platform_configs WHERE id=\? AND deleted_at IS NULL`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"id", "config_key", "name", "is_public", "status", "version"}).
			AddRow(id, "ui.theme", "Theme", true, "active", 4))
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	update, err := service.UpdateTarget(ctx, id)
	require.NoError(t, err)
	require.Equal(t, UpdateTarget{ID: id, Key: "ui.theme", Name: "Theme", Category: "ui", ValueType: "object", Value: json.RawMessage(`{"color":"blue"}`), Description: "Display theme", IsPublic: true, Status: "active", Version: 4}, update)
	deletion, err := service.DeleteTarget(ctx, id)
	require.NoError(t, err)
	require.Equal(t, DeleteTarget{ID: id, Key: "ui.theme", Name: "Theme", IsPublic: true, Status: "active", Version: 4}, deletion)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestActionTargetsRejectInvalidMissingAndFailedQueries(t *testing.T) {
	t.Parallel()
	id := "20f40b91-76f1-5d16-bade-fc464610cbde"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &Service{db: sqlx.NewDb(raw, "sqlmock")}
	_, err = service.UpdateTarget(t.Context(), id)
	require.Error(t, err)
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	for _, invalid := range []string{"", "not-a-uuid", "20F40B91-76F1-5D16-BADE-FC464610CBDE"} {
		_, err = service.UpdateTarget(ctx, invalid)
		require.ErrorIs(t, err, ErrInvalid)
		_, err = service.DeleteTarget(ctx, invalid)
		require.ErrorIs(t, err, ErrInvalid)
	}
	mock.ExpectQuery(`SELECT id,config_key,name,category,value_type,value,description,is_public,status,version FROM platform_configs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnError(sql.ErrNoRows)
	_, err = service.UpdateTarget(ctx, id)
	require.ErrorIs(t, err, ErrNotFound)
	mock.ExpectQuery(`SELECT id,config_key,name,is_public,status,version FROM platform_configs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnError(sql.ErrNoRows)
	_, err = service.DeleteTarget(ctx, id)
	require.ErrorIs(t, err, ErrNotFound)
	dbFailure := errors.New("database unavailable")
	mock.ExpectQuery(`SELECT id,config_key,name,is_public,status,version FROM platform_configs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).WillReturnError(dbFailure)
	_, err = service.DeleteTarget(ctx, id)
	require.ErrorIs(t, err, dbFailure)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestVersionedMutationsRejectStaleRows(t *testing.T) {
	t.Parallel()
	id := "20f40b91-76f1-5d16-bade-fc464610cbde"
	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			db := sqlx.NewDb(raw, "sqlmock")
			service := &Service{db: db, tx: database.NewTransactor(db)}
			now := time.Now()
			mock.ExpectQuery(`SELECT .* FROM platform_configs WHERE id=\? AND deleted_at IS NULL`).WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{"id", "config_key", "name", "category", "value_type", "value", "description", "is_public", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
					AddRow(id, "ui.theme", "Theme", "ui", "object", []byte(`{}`), "", true, "active", now, "creator", now, "updater", 5))
			mock.ExpectBegin()
			if operation == "update" {
				mock.ExpectExec(`UPDATE platform_configs SET .* WHERE id=\? AND version=\? AND deleted_at IS NULL`).
					WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), id, int64(4)).
					WillReturnResult(sqlmock.NewResult(0, 0))
			} else {
				mock.ExpectExec(`UPDATE platform_configs SET .* WHERE id=\? AND version=\? AND deleted_at IS NULL`).
					WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), id, int64(4)).
					WillReturnResult(sqlmock.NewResult(0, 0))
			}
			mock.ExpectRollback()
			ctx := platformprincipal.SystemContext(t.Context(), "admin")
			if operation == "update" {
				_, err = service.Update(ctx, UpdateInput{ID: id, Name: "Theme", Category: "ui", Value: []byte(`{}`), IsPublic: true, Status: "active", Version: 4})
			} else {
				err = service.Delete(ctx, id, 4)
			}
			require.ErrorIs(t, err, ErrStaleVersion)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
