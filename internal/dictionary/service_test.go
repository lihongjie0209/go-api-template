package dictionary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestValidateQuery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		query   Query
		wantErr bool
	}{
		{name: "valid", query: Query{Code: "identity.user", Sort: []Sort{{Field: "name", Direction: "asc"}}, Extension: json.RawMessage(`{"tenant_id":"t1"}`)}},
		{name: "single character code", query: Query{Code: "x"}},
		{name: "invalid code", query: Query{Code: "not a code"}, wantErr: true},
		{name: "unbounded ids", query: Query{Code: "identity.user", IDs: make([]string, 201)}, wantErr: true},
		{name: "unbounded values", query: Query{Code: "identity.user", Values: make([]string, 201)}, wantErr: true},
		{name: "blank value", query: Query{Code: "identity.user", Values: []string{"  "}}, wantErr: true},
		{name: "oversized value payload", query: Query{Code: "identity.user", Values: []string{string(make([]byte, 16*1024+1))}}, wantErr: true},
		{name: "exact value", query: Query{Code: "identity.user", Values: []string{"internal:id:1"}}},
		{name: "three distinct sort fields", query: Query{Code: "identity.user", Sort: []Sort{{Field: "name", Direction: "asc"}, {Field: "code", Direction: "desc"}, {Field: "sort_order", Direction: "asc"}}}},
		{name: "too many sort fields", query: Query{Code: "identity.user", Sort: []Sort{{Field: "name", Direction: "asc"}, {Field: "code", Direction: "asc"}, {Field: "sort_order", Direction: "asc"}, {Field: "name", Direction: "desc"}}}, wantErr: true},
		{name: "duplicate sort field", query: Query{Code: "identity.user", Sort: []Sort{{Field: "name", Direction: "asc"}, {Field: "name", Direction: "desc"}}}, wantErr: true},
		{name: "unknown sort", query: Query{Code: "identity.user", Sort: []Sort{{Field: "created_at", Direction: "asc"}}}, wantErr: true},
		{name: "extension must be object", query: Query{Code: "identity.user", Extension: json.RawMessage(`[]`)}, wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateQuery(tt.query)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalid)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestInputBounds(t *testing.T) {
	t.Parallel()
	require.False(t, validDefinition(DefinitionInput{Code: "valid", Name: "name", Description: string(make([]byte, maxDescriptionLength+1)), Kind: KindEnum, Source: SourceStatic, Status: "active"}))
	require.False(t, validItem(ItemInput{DictionaryID: uuidString(1), Code: "valid", Name: "name", Value: string(make([]byte, maxValueLength+1))}))
}

func TestCreateItemRejectsDuplicateSelectableValue(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, raw.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db := sqlx.NewDb(raw, "sqlmock")
	service := &Service{db: db, tx: database.NewTransactor(db)}
	dictionaryID := uuidString(1)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT dictionary_type,source_type FROM dictionary_definitions WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(dictionaryID).
		WillReturnRows(sqlmock.NewRows([]string{"dictionary_type", "source_type"}).AddRow(KindEnum, SourceStatic))
	mock.ExpectQuery(`SELECT id FROM dictionary_items WHERE dictionary_id=\? AND id<>\? AND deleted_at IS NULL AND COALESCE\(NULLIF\(value,''\),id\)=\? LIMIT 1 FOR UPDATE`).
		WithArgs(dictionaryID, sqlmock.AnyArg(), "shared").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuidString(2)))
	mock.ExpectRollback()
	mock.ExpectClose()
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeUser})
	_, err = service.CreateItem(ctx, ItemInput{DictionaryID: dictionaryID, Code: "new", Name: "New", Value: "shared"})
	require.ErrorIs(t, err, ErrConflict)
}

func TestEnsureSelectableValueAvailableUsesIDFallback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, value, effective string
	}{
		{name: "explicit value", value: "selected", effective: "selected"},
		{name: "empty value", value: "", effective: uuidString(2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, raw.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})
			db := sqlx.NewDb(raw, "sqlmock")
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id FROM dictionary_items WHERE dictionary_id=\? AND id<>\? AND deleted_at IS NULL AND COALESCE\(NULLIF\(value,''\),id\)=\? LIMIT 1 FOR UPDATE`).
				WithArgs(uuidString(1), uuidString(2), tc.effective).
				WillReturnRows(sqlmock.NewRows([]string{"id"}))
			mock.ExpectRollback()
			mock.ExpectClose()
			tx, err := db.BeginTxx(t.Context(), nil)
			require.NoError(t, err)
			require.NoError(t, ensureSelectableValueAvailable(t.Context(), tx, uuidString(1), uuidString(2), tc.value))
			require.NoError(t, tx.Rollback())
		})
	}
}

func TestUpdateItemLocksDefinitionBeforeCheckingSelectableValue(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, raw.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db := sqlx.NewDb(raw, "sqlmock")
	service := &Service{db: db, tx: database.NewTransactor(db)}
	dictionaryID, itemID := uuidString(1), uuidString(2)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT dictionary_id FROM dictionary_items WHERE id=\? AND deleted_at IS NULL$`).
		WithArgs(itemID).
		WillReturnRows(sqlmock.NewRows([]string{"dictionary_id"}).AddRow(dictionaryID))
	mock.ExpectQuery(`SELECT dictionary_type,source_type FROM dictionary_definitions WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(dictionaryID).
		WillReturnRows(sqlmock.NewRows([]string{"dictionary_type", "source_type"}).AddRow(KindEnum, SourceStatic))
	mock.ExpectQuery(`SELECT dictionary_id FROM dictionary_items WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(itemID).
		WillReturnRows(sqlmock.NewRows([]string{"dictionary_id"}).AddRow(dictionaryID))
	mock.ExpectQuery(`SELECT id FROM dictionary_items WHERE dictionary_id=\? AND id<>\? AND deleted_at IS NULL AND COALESCE\(NULLIF\(value,''\),id\)=\? LIMIT 1 FOR UPDATE`).
		WithArgs(dictionaryID, itemID, itemID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuidString(3)))
	mock.ExpectRollback()
	mock.ExpectClose()
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeUser})
	_, err = service.UpdateItem(ctx, ItemUpdate{ID: itemID, Name: "Updated", Value: "", Version: 1})
	require.ErrorIs(t, err, ErrConflict)
}

func TestDeleteItemLocksDefinitionBeforeItem(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, raw.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db := sqlx.NewDb(raw, "sqlmock")
	service := &Service{db: db, tx: database.NewTransactor(db)}
	dictionaryID, itemID := uuidString(1), uuidString(2)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT dictionary_id FROM dictionary_items WHERE id=\? AND deleted_at IS NULL$`).
		WithArgs(itemID).
		WillReturnRows(sqlmock.NewRows([]string{"dictionary_id"}).AddRow(dictionaryID))
	mock.ExpectQuery(`SELECT id FROM dictionary_definitions WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(dictionaryID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(dictionaryID))
	mock.ExpectQuery(`SELECT dictionary_id FROM dictionary_items WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(itemID).
		WillReturnRows(sqlmock.NewRows([]string{"dictionary_id"}).AddRow(dictionaryID))
	mock.ExpectQuery(`SELECT id FROM dictionary_items WHERE parent_id=\? AND deleted_at IS NULL LIMIT 1 FOR UPDATE`).
		WithArgs(itemID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuidString(3)))
	mock.ExpectRollback()
	mock.ExpectClose()
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeUser})
	require.ErrorIs(t, service.DeleteItem(ctx, itemID, 1), ErrConflict)
}

func TestPresentItemsResolvesParentNamesWithinDictionary(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, raw.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	parentID := uuidString(1)
	mock.ExpectQuery(`SELECT id,dictionary_id,name FROM dictionary_items WHERE id IN \(\?\) AND deleted_at IS NULL`).
		WithArgs(parentID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "dictionary_id", "name"}).AddRow(parentID, "dictionary-1", "Root"))
	mock.ExpectClose()
	service := &Service{db: sqlx.NewDb(raw, "sqlmock")}
	items := []Item{
		{ID: uuidString(2), DictionaryID: "dictionary-1", ParentID: &parentID},
		{ID: uuidString(3), DictionaryID: "dictionary-2", ParentID: &parentID},
		{ID: parentID, DictionaryID: "dictionary-1"},
	}

	require.NoError(t, service.presentItems(context.Background(), items))
	require.Equal(t, "Root", items[0].ParentName)
	require.Empty(t, items[1].ParentName)
	require.Empty(t, items[2].ParentName)
}

func TestPresentItemsSkipsParentQueryForRoots(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, raw.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectClose()
	service := &Service{db: sqlx.NewDb(raw, "sqlmock")}
	items := []Item{{ID: uuidString(1), DictionaryID: "dictionary-1"}}

	require.NoError(t, service.presentItems(context.Background(), items))
	require.Empty(t, items[0].ParentName)
}

func TestPresentItemsPropagatesParentLookupFailure(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, raw.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	parentID := uuidString(1)
	mock.ExpectQuery(`SELECT id,dictionary_id,name FROM dictionary_items WHERE id IN \(\?\) AND deleted_at IS NULL`).
		WithArgs(parentID).
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectClose()
	service := &Service{db: sqlx.NewDb(raw, "sqlmock")}
	items := []Item{{ID: uuidString(2), DictionaryID: "dictionary-1", ParentID: &parentID}}

	err = service.presentItems(context.Background(), items)
	require.ErrorContains(t, err, "load dictionary parent names")
}

func TestPageItemsExcludesCurrentSubtreeBeforeCountAndPagination(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, raw.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	dictionaryID, currentID := uuidString(1), uuidString(2)
	const recursiveQuery = `(?s)WITH RECURSIVE excluded\(id\) AS \(.*UNION.*child\.dictionary_id=\?.*\) SELECT `
	mock.ExpectQuery(recursiveQuery+`COUNT\(\*\) FROM dictionary_items WHERE .*id NOT IN \(SELECT id FROM excluded\)`).
		WithArgs(currentID, dictionaryID, dictionaryID, dictionaryID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(recursiveQuery+`.* FROM dictionary_items WHERE .*id NOT IN \(SELECT id FROM excluded\).*ORDER BY sort_order,code,id LIMIT \? OFFSET \?`).
		WithArgs(currentID, dictionaryID, dictionaryID, dictionaryID, 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectClose()
	service := &Service{db: sqlx.NewDb(raw, "sqlmock")}

	result, err := service.PageItems(platformprincipal.SystemContext(t.Context(), "test"), ItemPageInput{
		DictionaryID: dictionaryID, ExcludeSubtreeID: currentID,
	})
	require.NoError(t, err)
	require.Zero(t, result.Total)
	require.Empty(t, result.Items)
}

func TestPageItemsRejectsInvalidExcludedSubtreeID(t *testing.T) {
	t.Parallel()
	service := &Service{}
	_, err := service.PageItems(platformprincipal.SystemContext(t.Context(), "test"), ItemPageInput{
		DictionaryID: uuidString(1), ExcludeSubtreeID: "not-a-uuid",
	})
	require.ErrorIs(t, err, ErrInvalid)
}

func TestBuildTree(t *testing.T) {
	t.Parallel()
	rootID, childID := "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"
	items := []Item{{ID: childID, ParentID: &rootID, Code: "child", SortOrder: 2}, {ID: rootID, Code: "root", SortOrder: 1}}
	got, err := buildTree(items, nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, rootID, got[0].ID)
	require.Len(t, got[0].Children, 1)
	require.Equal(t, childID, got[0].Children[0].ID)
}

type testProvider struct{}

func (testProvider) Query(context.Context, Query) (Result, error) {
	return Result{Code: "identity.user", Type: KindEnum}, nil
}
func TestProviderRegistry(t *testing.T) {
	t.Parallel()
	r := NewProviderRegistry()
	require.NoError(t, r.Register("identity.user", testProvider{}))
	require.ErrorIs(t, r.Register("identity.user", testProvider{}), ErrConflict)
	result, err := r.Query(context.Background(), Query{Code: "identity.user"})
	require.NoError(t, err)
	require.Equal(t, "identity.user", result.Code)
}

func TestProviderRegistryRejectsTypedNilProvider(t *testing.T) {
	t.Parallel()
	registry := NewProviderRegistry()
	var provider *testProvider
	require.ErrorIs(t, registry.Register("identity.user", provider), ErrInvalid)
	require.False(t, registry.Has("identity.user"))
}

func TestCreateDefinitionRejectsUnregisteredProvider(t *testing.T) {
	t.Parallel()
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeUser})
	valid := DefinitionInput{Code: "identity.user", Name: "Users", Kind: KindEnum, Source: SourceProvider, Status: "active"}

	for _, registry := range []*ProviderRegistry{nil, NewProviderRegistry()} {
		service := &Service{providers: registry}
		_, err := service.CreateDefinition(ctx, valid)
		require.ErrorIs(t, err, ErrProviderNotRegistered)
	}
}

func TestProviderRegistryHas(t *testing.T) {
	t.Parallel()
	registry := NewProviderRegistry()
	require.False(t, registry.Has("identity.user"))
	require.NoError(t, registry.Register("identity.user", testProvider{}))
	require.True(t, registry.Has("identity.user"))
	require.False(t, registry.Has("identity.other"))
}

func TestValidProviderResultRejectsDuplicateAndOversizedTrees(t *testing.T) {
	t.Parallel()
	id := "00000000-0000-0000-0000-000000000001"
	valid := Result{Code: "identity.user", Name: "Users", Type: KindTree, Total: 1, Extension: json.RawMessage(`{}`), Items: []ResultItem{{ID: id, Code: "root", Name: "Root", Extension: json.RawMessage(`{}`)}}}
	require.True(t, validProviderResult(valid, Query{}, 10))
	valid.Items[0].Children = []ResultItem{{ID: id, Code: "duplicate", Extension: json.RawMessage(`{}`)}}
	require.False(t, validProviderResult(valid, Query{}, 10))
	require.False(t, validProviderResult(Result{Type: KindEnum, Total: 201, Items: make([]ResultItem, 201)}, Query{}, 1000))
}

func TestValidProviderResultRejectsDuplicateEffectiveValues(t *testing.T) {
	t.Parallel()
	firstID, secondID := uuidString(1), uuidString(2)
	items := []ResultItem{
		{ID: firstID, Code: "first", Name: "First", Value: "", Extension: json.RawMessage(`{}`)},
		{ID: secondID, Code: "second", Name: "Second", Value: firstID, Extension: json.RawMessage(`{}`)},
	}
	for _, kind := range []Kind{KindEnum, KindTree} {
		t.Run(string(kind), func(t *testing.T) {
			result := Result{Code: "values", Name: "Values", Type: kind, Items: items, Total: 2, Extension: json.RawMessage(`{}`)}
			if kind == KindEnum {
				result.Page, result.PageSize = 1, 20
			}
			require.False(t, validProviderResult(result, Query{}, 10))
		})
	}
}

func TestFilterTreeItemsRetainsAncestorsAndRejectsDisabledSubtrees(t *testing.T) {
	t.Parallel()
	rootID, childID := uuidString(1), uuidString(2)
	items := []Item{{ID: rootID, Code: "root", Name: "Root"}, {ID: childID, ParentID: &rootID, Code: "child", Name: "Matched child"}}
	filtered := filterTreeItems(items, Query{Keyword: "matched"})
	require.Len(t, filtered, 2)
	items[0].Disabled = true
	require.Empty(t, filterTreeItems(items, Query{Keyword: "matched"}))
}

func TestFilterTreeItemsByExactValueRetainsAncestors(t *testing.T) {
	t.Parallel()
	rootID, childID, otherID := uuidString(1), uuidString(2), uuidString(3)
	items := []Item{
		{ID: rootID, Code: "root", Value: "root-value"},
		{ID: childID, ParentID: &rootID, Code: "child", Value: "selected-value"},
		{ID: otherID, Code: "other", Value: "other-value"},
	}

	filtered := filterTreeItems(items, Query{Values: []string{"selected-value"}})
	require.Len(t, filtered, 2)
	require.Equal(t, []string{rootID, childID}, []string{filtered[0].ID, filtered[1].ID})
}

func TestFilterTreeItemsByIDWhenValueIsEmpty(t *testing.T) {
	t.Parallel()
	rootID, childID := uuidString(1), uuidString(2)
	items := []Item{
		{ID: rootID, Code: "root"},
		{ID: childID, ParentID: &rootID, Code: "child"},
	}

	filtered := filterTreeItems(items, Query{Values: []string{childID}})
	require.Len(t, filtered, 2)
	require.Equal(t, []string{rootID, childID}, []string{filtered[0].ID, filtered[1].ID})
}

func TestQueryStaticEnumFiltersExactValuesInCountAndPage(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, raw.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	const selected = "x' OR 1=1 --"
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dictionary_items WHERE .*COALESCE\(NULLIF\(value,''\),id\) IN \(\?\)`).
		WithArgs("dictionary-1", selected).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT .* FROM dictionary_items WHERE .*COALESCE\(NULLIF\(value,''\),id\) IN \(\?\).*LIMIT \? OFFSET \?`).
		WithArgs("dictionary-1", selected, 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectClose()
	service := &Service{db: sqlx.NewDb(raw, "sqlmock")}

	result, err := service.queryStatic(context.Background(), Definition{ID: "dictionary-1", Kind: KindEnum}, Query{Values: []string{selected}})
	require.NoError(t, err)
	require.Zero(t, result.Total)
	require.Empty(t, result.Items)
}

func TestQueryStaticEnumTreatsKeywordWildcardsAsLiteral(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, raw.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	pattern := "%!%!_!!%"
	where := `dictionary_id=\? AND deleted_at IS NULL AND disabled=false AND \(lower\(code\) LIKE \? ESCAPE '!' OR lower\(name\) LIKE \? ESCAPE '!'\)`
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dictionary_items WHERE `+where).
		WithArgs("dictionary-1", pattern, pattern).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT .* FROM dictionary_items WHERE `+where+` ORDER BY .* LIMIT \? OFFSET \?`).
		WithArgs("dictionary-1", pattern, pattern, 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectClose()
	service := &Service{db: sqlx.NewDb(raw, "sqlmock")}
	result, err := service.queryStatic(t.Context(), Definition{ID: "dictionary-1", Kind: KindEnum}, Query{Keyword: "%_!"})
	require.NoError(t, err)
	require.Zero(t, result.Total)
}

func TestBuildTreeRejectsCycleAndReturnsEmptyArray(t *testing.T) {
	t.Parallel()
	firstID, secondID := uuidString(1), uuidString(2)
	_, err := buildTree([]Item{{ID: firstID, ParentID: &secondID}, {ID: secondID, ParentID: &firstID}}, nil)
	require.ErrorIs(t, err, ErrInvalid)
	empty, err := buildTree(nil, nil)
	require.NoError(t, err)
	require.NotNil(t, empty)
	require.Empty(t, empty)
}

func TestBuildTreeHonorsRequestedSort(t *testing.T) {
	t.Parallel()
	items := []Item{{ID: uuidString(1), Code: "b", Name: "Alpha"}, {ID: uuidString(2), Code: "a", Name: "Zulu"}}
	tree, err := buildTree(items, []Sort{{Field: "name", Direction: "desc"}})
	require.NoError(t, err)
	require.Equal(t, "Zulu", tree[0].Name)
}

func TestValidProviderResultEnforcesPublicContract(t *testing.T) {
	t.Parallel()
	id := uuidString(1)
	result := Result{Code: "gender", Name: "Gender", Type: KindEnum, Page: 1, PageSize: 20, Total: 1, Extension: json.RawMessage(`{}`), Items: []ResultItem{{ID: id, Code: "m", Name: "Male", Value: "m", Extension: json.RawMessage(`{}`)}}}
	require.True(t, validProviderResult(result, Query{}, 100))
	result.Items[0].Disabled = true
	require.False(t, validProviderResult(result, Query{}, 100))
	require.True(t, validProviderResult(result, Query{IncludeDisabled: true}, 100))
	require.True(t, validProviderResult(result, Query{IncludeDisabled: true, Values: []string{"m"}}, 100))
	require.False(t, validProviderResult(result, Query{IncludeDisabled: true, Values: []string{"other"}}, 100))
}

func TestValidProviderResultEnforcesEveryEnumFilter(t *testing.T) {
	t.Parallel()
	item := ResultItem{ID: uuidString(1), Code: "north", Name: "North Region", Value: "n", Extension: json.RawMessage(`{}`)}
	result := Result{Code: "regions", Name: "Regions", Type: KindEnum, Page: 1, PageSize: 20, Total: 1, Extension: json.RawMessage(`{}`), Items: []ResultItem{item}}
	for _, test := range []struct {
		name  string
		query Query
		valid bool
	}{
		{name: "all matching", query: Query{Keyword: "REGION", IDs: []string{item.ID}, Codes: []string{"north"}, Values: []string{"n"}}, valid: true},
		{name: "wrong keyword", query: Query{Keyword: "south"}},
		{name: "wrong ID", query: Query{IDs: []string{uuidString(2)}}},
		{name: "wrong code", query: Query{Codes: []string{"south"}}},
		{name: "wrong value", query: Query{Values: []string{"s"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.valid, validProviderResult(result, test.query, 10))
		})
	}
}

func TestValidProviderResultTreeAllowsOnlyMatchingNodesAndAncestors(t *testing.T) {
	t.Parallel()
	rootID := uuidString(1)
	child := ResultItem{ID: uuidString(2), ParentID: &rootID, Code: "target", Name: "Target", Extension: json.RawMessage(`{}`)}
	result := Result{Code: "regions", Name: "Regions", Type: KindTree, Total: 2, Extension: json.RawMessage(`{}`), Items: []ResultItem{{ID: rootID, Code: "root", Name: "Root", Extension: json.RawMessage(`{}`), Children: []ResultItem{child}}}}
	require.True(t, validProviderResult(result, Query{Keyword: "target", IDs: []string{child.ID}, Codes: []string{"target"}}, 10))
	result.Items[0].Children = append(result.Items[0].Children, ResultItem{ID: uuidString(3), ParentID: &rootID, Code: "other", Name: "Other", Extension: json.RawMessage(`{}`)})
	result.Total = 3
	require.False(t, validProviderResult(result, Query{Keyword: "target"}, 10))
}

func TestValidProviderResultRequiresStableRequestedOrder(t *testing.T) {
	t.Parallel()
	result := Result{Code: "regions", Name: "Regions", Type: KindEnum, Page: 1, PageSize: 20, Total: 2, Extension: json.RawMessage(`{}`), Items: []ResultItem{
		{ID: uuidString(2), Code: "b", Name: "Beta", SortOrder: 1, Extension: json.RawMessage(`{}`)},
		{ID: uuidString(1), Code: "a", Name: "Alpha", SortOrder: 1, Extension: json.RawMessage(`{}`)},
	}}
	require.False(t, validProviderResult(result, Query{}, 10))
	require.True(t, validProviderResult(result, Query{Sort: []Sort{{Field: "name", Direction: "desc"}}}, 10))
	result.Items[0], result.Items[1] = result.Items[1], result.Items[0]
	require.True(t, validProviderResult(result, Query{}, 10))
	require.False(t, validProviderResult(result, Query{Sort: []Sort{{Field: "name", Direction: "desc"}}}, 10))
}

func TestQueryStaticEnumAddsStableIDSortTieBreaker(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		sort  []Sort
		order string
	}{
		{name: "default order", order: `sort_order ASC,code ASC,id ASC`},
		{name: "requested order", sort: []Sort{{Field: "name", Direction: "desc"}}, order: `name DESC,id ASC`},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, raw.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dictionary_items`).WithArgs("dictionary-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
			mock.ExpectQuery(`ORDER BY `+test.order+` LIMIT \? OFFSET \?`).WithArgs("dictionary-1", 20, 0).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			mock.ExpectClose()
			service := &Service{db: sqlx.NewDb(raw, "sqlmock")}
			_, err = service.queryStatic(t.Context(), Definition{ID: "dictionary-1", Kind: KindEnum}, Query{Sort: test.sort})
			require.NoError(t, err)
		})
	}
}

func TestQueryStaticTreeTotalCountsFilteredNodes(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, raw.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	dictionaryID, rootID := uuidString(4), uuidString(1)
	now := time.Now()
	rows := sqlmock.NewRows(strings.Split(itemColumns, ","))
	rows.AddRow(rootID, dictionaryID, nil, "root", "Root", "", false, 1, []byte(`{}`), now, "system", now, "system", 1)
	rows.AddRow(uuidString(2), dictionaryID, rootID, "target", "Target", "", false, 1, []byte(`{}`), now, "system", now, "system", 1)
	rows.AddRow(uuidString(3), dictionaryID, nil, "other", "Other", "", false, 2, []byte(`{}`), now, "system", now, "system", 1)
	mock.ExpectQuery(`SELECT .* FROM dictionary_items WHERE dictionary_id=\? AND deleted_at IS NULL ORDER BY .* LIMIT \?`).WithArgs(dictionaryID, 11).WillReturnRows(rows)
	mock.ExpectClose()
	service := &Service{db: sqlx.NewDb(raw, "sqlmock"), cfg: config.Config{Dictionary: config.Dictionary{MaxTreeNodes: 10}}}
	result, err := service.queryStatic(t.Context(), Definition{ID: dictionaryID, Code: "regions", Name: "Regions", Kind: KindTree}, Query{Keyword: "target"})
	require.NoError(t, err)
	require.Equal(t, int64(2), result.Total)
	require.Len(t, result.Items, 1)
	require.Len(t, result.Items[0].Children, 1)
}

func uuidString(last byte) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", last)
}
