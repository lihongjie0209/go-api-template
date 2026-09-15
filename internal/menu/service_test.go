package menu

import (
	"context"
	"encoding/json"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/microservice-platform-go/stableid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestValidate(t *testing.T) {
	permission := "p"
	tests := []struct {
		name    string
		input   Input
		wantErr bool
	}{{"directory", Input{Key: "menu:system", Name: "System", Type: "directory", Status: "active", Metadata: json.RawMessage(`{}`)}, false}, {"page requires component", Input{Key: "menu:users", Name: "Users", Type: "page", RoutePath: "/users", Status: "active", Metadata: json.RawMessage(`{}`)}, true}, {"button requires parent", Input{Key: "button:user:create", Name: "Create", Type: "button", PermissionID: &permission, Status: "active", Metadata: json.RawMessage(`{}`)}, true}, {"external requires safe URL", Input{Key: "menu:docs", Name: "Docs", Type: "external", ExternalURL: "javascript:alert(1)", Status: "active", Metadata: json.RawMessage(`{}`)}, true}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validate(test.input)
			if test.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
func TestBuildRejectsCycle(t *testing.T) {
	a, b := "a", "b"
	_, err := build([]Record{{ID: a, ParentID: &b}, {ID: b, ParentID: &a}})
	require.Error(t, err)
}
func TestMenuStableIDGoldenMapping(t *testing.T) {
	generator, err := stableid.New(menuNamespace)
	require.NoError(t, err)
	id, err := generator.String("menu:system:user-management")
	require.NoError(t, err)
	require.Equal(t, "abb887e7-57fd-5ab9-8940-04b1b0b972dc", id)
}

func TestFilterByPermissionsIncludesAncestorsAndRejectsOrphans(t *testing.T) {
	root, child, orphan := "root", "child", "orphan"
	permission := "permission-1"
	records := []Record{{ID: root}, {ID: child, ParentID: &root, PermissionID: &permission}, {ID: orphan, ParentID: ptr("disabled-parent"), PermissionID: &permission}}
	filtered := filterByPermissions(records, map[string]struct{}{permission: {}})
	require.Equal(t, []Record{records[0], records[1]}, filtered)
}

func TestFilterByPermissionsDoesNotBypassProtectedAncestor(t *testing.T) {
	root, child := "root", "child"
	required := "permission-admin"
	records := []Record{{ID: root, PermissionID: &required}, {ID: child, ParentID: &root}}
	require.Empty(t, filterByPermissions(records, map[string]struct{}{}))
}

func TestFilterRecordsRetainsAncestors(t *testing.T) {
	root, child := "root", "child"
	records := []Record{
		{ID: root, Key: "menu:system", Name: "System", Type: "directory", Status: "active"},
		{ID: child, ParentID: &root, Key: "menu:users", Name: "User Management", Type: "page", Status: "active", RoutePath: "/users"},
	}
	filtered := filterRecords(records, TreeInput{Keyword: "management", Types: []string{"page"}})
	require.Equal(t, records, filtered)
}

func TestValidateTreeInputRejectsUnboundedAndInvalidFilters(t *testing.T) {
	to := time.Now()
	from := to.Add(time.Hour)
	for _, input := range []TreeInput{
		{IDs: make([]string, 201)},
		{Types: []string{"unknown"}},
		{Statuses: []string{"unknown"}},
		{CreatedAtFrom: &from, CreatedAtTo: &to},
	} {
		require.ErrorIs(t, validateTreeInput(input), ErrInvalid)
	}
}

func ptr(value string) *string { return &value }

func TestListCachesMenuTreeSource(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	service := &Service{
		db:    sqlx.NewDb(db, "sqlmock"),
		cache: cache.NewRedisStore(redisClient),
		cfg:   config.Config{Menu: config.Menu{CacheTTL: time.Minute, MaxNodes: 100}},
	}
	rows := sqlmock.NewRows([]string{"id", "parent_id", "menu_key", "name", "menu_type", "route_path", "component", "external_url", "icon", "permission_id", "visible", "status", "sort_order", "metadata", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow("menu-1", nil, "menu:users", "Users", "page", "/users", "users/index", "", "", nil, true, "active", 1, []byte(`{}`), time.Now(), "actor", time.Now(), "actor", 1)
	mock.ExpectQuery(`SELECT id,parent_id,menu_key`).WillReturnRows(rows)

	first, err := service.list(context.Background(), false)
	require.NoError(t, err)
	second, err := service.list(context.Background(), false)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Len(t, second, 1)
	require.Equal(t, first[0].ID, second[0].ID)
	require.True(t, first[0].CreatedAt.Equal(second[0].CreatedAt))
	require.True(t, redisServer.Exists(menuCacheAll))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInvalidateCacheRemovesBothMenuViews(t *testing.T) {
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	store := cache.NewRedisStore(redisClient)
	require.NoError(t, store.Set(t.Context(), menuCacheAll, []byte(`[]`), time.Minute))
	require.NoError(t, store.Set(t.Context(), menuCacheVisible, []byte(`[]`), time.Minute))

	(&Service{cache: store}).invalidateCache(t.Context())
	require.False(t, redisServer.Exists(menuCacheAll))
	require.False(t, redisServer.Exists(menuCacheVisible))
}
