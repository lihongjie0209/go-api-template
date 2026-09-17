package application

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/dictionary"
	"github.com/stretchr/testify/require"
)

func TestDictionaryDefinitionIDGoldenMapping(t *testing.T) {
	t.Parallel()
	id, err := DictionaryDefinitionID()
	require.NoError(t, err)
	require.Equal(t, "460e0478-4662-577b-ac5b-8d56ee2b4625", id)
}

func TestDictionaryProviderQueriesBoundedPublicMetadata(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery(`SELECT count\(\*\) FROM applications WHERE deleted_at IS NULL AND status='active'.*LOWER\(code\) LIKE`).
		WithArgs("%console%", "%console%").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT id,code,name,icon,home_path,status,sort_order FROM applications.*ORDER BY name ASC,id ASC LIMIT \? OFFSET \?`).
		WithArgs("%console%", "%console%", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "icon", "home_path", "status", "sort_order"}).AddRow("319595d8-2486-54cb-a522-82b9d6b2d36a", "console", "Console", "console", "/", "active", 1))
	result, err := NewDictionaryProvider(db).Query(context.Background(), dictionary.Query{Keyword: "console", Sort: []dictionary.Sort{{Field: "name", Direction: "asc"}}})
	require.NoError(t, err)
	require.Equal(t, DictionaryCode, result.Code)
	require.Equal(t, int64(1), result.Total)
	require.Len(t, result.Items, 1)
	require.Equal(t, "Console", result.Items[0].Name)
	require.Contains(t, string(result.Items[0].Extension), `"home_path":"/"`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRegisterDictionaryProvider(t *testing.T) {
	t.Parallel()
	registry := dictionary.NewProviderRegistry()
	require.NoError(t, RegisterDictionaryProvider(registry, &DictionaryProvider{}))
	require.ErrorIs(t, RegisterDictionaryProvider(registry, &DictionaryProvider{}), dictionary.ErrConflict)
}
