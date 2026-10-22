package dictionary

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type unavailableDictionaryActors struct{}

func (unavailableDictionaryActors) ResolveUserIDs(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("identity display unavailable")
}

func TestCreateDefinitionRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := &Service{db: db, tx: database.NewTransactor(db)}
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO dictionary_definitions`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM dictionary_definitions WHERE id=\? AND deleted_at IS NULL`).
		WithArgs(sqlmock.AnyArg()).WillReturnError(errors.New("result read unavailable"))
	mock.ExpectRollback()

	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	_, err = service.CreateDefinition(ctx, DefinitionInput{Code: "region", Name: "Region", Kind: KindEnum, Source: SourceStatic, Status: "active"})
	require.ErrorContains(t, err, "result read unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCreateItemRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := &Service{db: db, tx: database.NewTransactor(db), cfg: config.Config{Dictionary: config.Dictionary{MaxTreeNodes: 100}}}
	dictionaryID := uuidString(1)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT dictionary_type,source_type FROM dictionary_definitions WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(dictionaryID).WillReturnRows(sqlmock.NewRows([]string{"dictionary_type", "source_type"}).AddRow(KindEnum, SourceStatic))
	mock.ExpectQuery(`SELECT id FROM dictionary_items WHERE dictionary_id=\? AND id<>\? AND deleted_at IS NULL`).
		WithArgs(dictionaryID, sqlmock.AnyArg(), "new").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`INSERT INTO dictionary_items`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE dictionary_definitions SET updated_at=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM dictionary_items WHERE id=\? AND deleted_at IS NULL`).
		WithArgs(sqlmock.AnyArg()).WillReturnError(errors.New("result read unavailable"))
	mock.ExpectRollback()

	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	_, err = service.CreateItem(ctx, ItemInput{DictionaryID: dictionaryID, Code: "new", Name: "New", Value: "new"})
	require.ErrorContains(t, err, "result read unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateDefinitionRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := &Service{db: db, tx: database.NewTransactor(db)}
	id := uuidString(1)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE dictionary_definitions SET name=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM dictionary_definitions WHERE id=\? AND deleted_at IS NULL`).
		WithArgs(id).WillReturnError(errors.New("result read unavailable"))
	mock.ExpectRollback()

	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	_, err = service.UpdateDefinition(ctx, DefinitionUpdate{ID: id, Name: "Region", Status: "active", Version: 2})
	require.ErrorContains(t, err, "result read unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateItemRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := &Service{db: db, tx: database.NewTransactor(db)}
	dictionaryID, itemID := uuidString(1), uuidString(2)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT dictionary_id FROM dictionary_items WHERE id=\? AND deleted_at IS NULL`).
		WithArgs(itemID).WillReturnRows(sqlmock.NewRows([]string{"dictionary_id"}).AddRow(dictionaryID))
	mock.ExpectQuery(`SELECT dictionary_type,source_type FROM dictionary_definitions WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(dictionaryID).WillReturnRows(sqlmock.NewRows([]string{"dictionary_type", "source_type"}).AddRow(KindEnum, SourceStatic))
	mock.ExpectQuery(`SELECT dictionary_id FROM dictionary_items WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(itemID).WillReturnRows(sqlmock.NewRows([]string{"dictionary_id"}).AddRow(dictionaryID))
	mock.ExpectQuery(`SELECT id FROM dictionary_items WHERE dictionary_id=\? AND id<>\? AND deleted_at IS NULL`).
		WithArgs(dictionaryID, itemID, "new").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`UPDATE dictionary_items SET parent_id=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE dictionary_definitions SET updated_at=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM dictionary_items WHERE id=\? AND deleted_at IS NULL`).
		WithArgs(itemID).WillReturnError(errors.New("result read unavailable"))
	mock.ExpectRollback()

	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	_, err = service.UpdateItem(ctx, ItemUpdate{ID: itemID, Name: "New", Value: "new", Version: 2})
	require.ErrorContains(t, err, "result read unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCreateItemPreservesParentDisplayWhenActorLookupFailsAfterCommit(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := &Service{db: db, tx: database.NewTransactor(db), actors: unavailableDictionaryActors{}, cfg: config.Config{Dictionary: config.Dictionary{MaxTreeNodes: 100}}}
	dictionaryID, parentID, itemID := uuidString(1), uuidString(2), uuidString(3)
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT dictionary_type,source_type FROM dictionary_definitions WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(dictionaryID).WillReturnRows(sqlmock.NewRows([]string{"dictionary_type", "source_type"}).AddRow(KindTree, SourceStatic))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dictionary_items WHERE dictionary_id=\? AND deleted_at IS NULL`).
		WithArgs(dictionaryID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dictionary_items WHERE id=\? AND dictionary_id=\? AND deleted_at IS NULL`).
		WithArgs(parentID, dictionaryID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT id FROM dictionary_items WHERE dictionary_id=\? AND id<>\? AND deleted_at IS NULL`).
		WithArgs(dictionaryID, sqlmock.AnyArg(), "child").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`INSERT INTO dictionary_items`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`WITH RECURSIVE tree_nodes`).WillReturnRows(sqlmock.NewRows([]string{"total", "reachable", "max_depth"}).AddRow(2, 2, 2))
	mock.ExpectExec(`UPDATE dictionary_definitions SET updated_at=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM dictionary_items WHERE id=\? AND deleted_at IS NULL`).
		WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"id", "dictionary_id", "parent_id", "code", "name", "value", "disabled", "sort_order", "extension", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow(itemID, dictionaryID, parentID, "child", "Child", "child", false, 0, []byte("{}"), now, "admin", now, "admin", 1))
	mock.ExpectQuery(`SELECT name FROM dictionary_items WHERE id=\? AND dictionary_id=\? AND deleted_at IS NULL`).
		WithArgs(parentID, dictionaryID).WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Parent"))
	mock.ExpectCommit()

	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	item, err := service.CreateItem(ctx, ItemInput{DictionaryID: dictionaryID, ParentID: &parentID, Code: "child", Name: "Child", Value: "child"})
	require.NoError(t, err)
	require.Equal(t, parentID, *item.ParentID)
	require.Equal(t, "Parent", item.ParentName)
	require.Equal(t, "admin", item.CreatedByName)
	require.Equal(t, int64(1), item.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}
