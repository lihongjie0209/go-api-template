package dictionary

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

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

func TestValidProviderResultRejectsDuplicateAndOversizedTrees(t *testing.T) {
	t.Parallel()
	id := "00000000-0000-0000-0000-000000000001"
	valid := Result{Code: "identity.user", Name: "Users", Type: KindTree, Total: 1, Extension: json.RawMessage(`{}`), Items: []ResultItem{{ID: id, Code: "root", Name: "Root", Extension: json.RawMessage(`{}`)}}}
	require.True(t, validProviderResult(valid, Query{}, 10))
	valid.Items[0].Children = []ResultItem{{ID: id, Code: "duplicate", Extension: json.RawMessage(`{}`)}}
	require.False(t, validProviderResult(valid, Query{}, 10))
	require.False(t, validProviderResult(Result{Type: KindEnum, Total: 201, Items: make([]ResultItem, 201)}, Query{}, 1000))
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
	result := Result{Code: "gender", Name: "Gender", Type: KindEnum, Page: 1, PageSize: 20, Total: 1, Extension: json.RawMessage(`{}`), Items: []ResultItem{{ID: id, Code: "m", Name: "Male", Extension: json.RawMessage(`{}`)}}}
	require.True(t, validProviderResult(result, Query{}, 100))
	result.Items[0].Disabled = true
	require.False(t, validProviderResult(result, Query{}, 100))
	require.True(t, validProviderResult(result, Query{IncludeDisabled: true}, 100))
}

func uuidString(last byte) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", last)
}
