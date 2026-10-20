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

func TestItemActionTargetsProjectOnlyNeededFields(t *testing.T) {
	t.Parallel()
	id, dictionaryID, parentID := uuidString(1), uuidString(2), uuidString(3)
	now := time.Now()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, raw.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	service := &Service{db: sqlx.NewDb(raw, "sqlmock")}
	itemRows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{
			"id", "dictionary_id", "parent_id", "code", "name", "value", "disabled", "sort_order", "extension",
			"created_at", "created_by", "updated_at", "updated_by", "version",
		}).AddRow(id, dictionaryID, parentID, "gd", "广东", "guangdong", false, 2, []byte(`{"region":"south"}`), now, "creator", now, "updater", 4)
	}
	for range 2 {
		mock.ExpectQuery(`SELECT id,dictionary_id,parent_id,code,name,value,disabled,sort_order,extension`).WithArgs(id).WillReturnRows(itemRows())
		mock.ExpectQuery(`SELECT id,dictionary_id,name FROM dictionary_items WHERE id IN`).WithArgs(parentID).
			WillReturnRows(sqlmock.NewRows([]string{"id", "dictionary_id", "name"}).AddRow(parentID, dictionaryID, "中国"))
	}
	mock.ExpectClose()
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	update, err := service.ItemUpdateTarget(ctx, id)
	require.NoError(t, err)
	require.Equal(t, ItemUpdateTarget{
		ID: id, DictionaryID: dictionaryID, ParentID: &parentID, ParentName: "中国",
		Code: "gd", Name: "广东", Value: "guangdong", SortOrder: 2,
		Extension: []byte(`{"region":"south"}`), Version: 4,
	}, update)
	deletion, err := service.ItemDeleteTarget(ctx, id)
	require.NoError(t, err)
	require.Equal(t, ItemDeleteTarget{ID: id, DictionaryID: dictionaryID, Code: "gd", Name: "广东", Version: 4}, deletion)
}

func TestItemMutationsReportStaleVersions(t *testing.T) {
	t.Parallel()
	id, dictionaryID := uuidString(1), uuidString(2)
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	for _, tc := range []struct {
		name  string
		setup func(sqlmock.Sqlmock)
		call  func(*Service) error
	}{
		{"update", func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(`SELECT dictionary_id FROM dictionary_items WHERE id=\? AND deleted_at IS NULL$`).WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{"dictionary_id"}).AddRow(dictionaryID))
			mock.ExpectQuery(`SELECT dictionary_type,source_type FROM dictionary_definitions`).WithArgs(dictionaryID).
				WillReturnRows(sqlmock.NewRows([]string{"dictionary_type", "source_type"}).AddRow("enum", "static"))
			mock.ExpectQuery(`SELECT dictionary_id FROM dictionary_items WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{"dictionary_id"}).AddRow(dictionaryID))
			mock.ExpectQuery(`SELECT id FROM dictionary_items WHERE dictionary_id=\? AND id<>\?`).WithArgs(dictionaryID, id, id).
				WillReturnError(sql.ErrNoRows)
			mock.ExpectExec(`UPDATE dictionary_items SET parent_id=`).WillReturnResult(sqlmock.NewResult(0, 0))
		}, func(service *Service) error {
			_, err := service.UpdateItem(ctx, ItemUpdate{ID: id, Name: "广东", Version: 4})
			return err
		}},
		{"delete", func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(`SELECT dictionary_id FROM dictionary_items WHERE id=\? AND deleted_at IS NULL$`).WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{"dictionary_id"}).AddRow(dictionaryID))
			mock.ExpectQuery(`SELECT id FROM dictionary_definitions WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).WithArgs(dictionaryID).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(dictionaryID))
			mock.ExpectQuery(`SELECT dictionary_id FROM dictionary_items WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{"dictionary_id"}).AddRow(dictionaryID))
			mock.ExpectQuery(`SELECT id FROM dictionary_items WHERE parent_id=\? AND deleted_at IS NULL LIMIT 1 FOR UPDATE`).WithArgs(id).
				WillReturnError(sql.ErrNoRows)
			mock.ExpectExec(`UPDATE dictionary_items SET deleted_at=`).WillReturnResult(sqlmock.NewResult(0, 0))
		}, func(service *Service) error { return service.DeleteItem(ctx, id, 4) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			db := sqlx.NewDb(raw, "sqlmock")
			service := &Service{db: db, tx: database.NewTransactor(db)}
			mock.ExpectBegin()
			tc.setup(mock)
			mock.ExpectRollback()
			require.ErrorIs(t, tc.call(service), ErrStaleVersion)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestItemActionTargetsRejectMissingAndInvalidIDs(t *testing.T) {
	t.Parallel()
	id := uuidString(1)
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, raw.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	service := &Service{db: sqlx.NewDb(raw, "sqlmock")}
	_, err = service.ItemUpdateTarget(t.Context(), id)
	require.Error(t, err)
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	_, err = service.ItemDeleteTarget(ctx, "invalid")
	require.ErrorIs(t, err, ErrInvalid)
	mock.ExpectQuery(`SELECT id,dictionary_id,parent_id,code,name,value,disabled,sort_order,extension`).WithArgs(id).WillReturnError(sql.ErrNoRows)
	mock.ExpectClose()
	_, err = service.ItemUpdateTarget(ctx, id)
	require.ErrorIs(t, err, ErrNotFound)
}
