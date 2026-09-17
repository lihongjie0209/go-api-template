package navigation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type memoryCache struct {
	mu     sync.Mutex
	values map[string][]byte
	sets   int
}

func (c *memoryCache) Get(_ context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.values[key]
	if !ok {
		return nil, errors.New("cache miss")
	}
	return append([]byte(nil), value...), nil
}
func (c *memoryCache) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = make(map[string][]byte)
	}
	c.values[key] = append([]byte(nil), value...)
	c.sets++
	return nil
}
func (c *memoryCache) SetIfAbsent(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	_, exists := c.values[key]
	c.mu.Unlock()
	if exists {
		return false, nil
	}
	return true, c.Set(ctx, key, value, ttl)
}
func (c *memoryCache) Delete(_ context.Context, keys ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range keys {
		delete(c.values, key)
	}
	return nil
}
func (c *memoryCache) Exists(_ context.Context, key string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.values[key]
	return ok, nil
}

func TestValidTreeInput(t *testing.T) {
	t.Parallel()
	require.True(t, validTreeInput(TreeInput{
		ApplicationID: "app-1",
		Keyword:       "member",
		Types:         []string{"directory", "menu"},
		Statuses:      []string{"active"},
	}))
	require.False(t, validTreeInput(TreeInput{}))
	require.False(t, validTreeInput(TreeInput{ApplicationID: "app-1", Types: []string{"button"}}))
	require.False(t, validTreeInput(TreeInput{ApplicationID: "app-1", Statuses: []string{"unknown"}}))
}

func TestCurrentTreeScopesGrantAndMembershipInSQL(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery(`FROM applications a JOIN tenant_application_grants g.*g.tenant_id=\?.*JOIN tenant_memberships m.*LEFT JOIN navigations n.*a.id=\?`).
		WithArgs("tenant-1", sqlmock.AnyArg(), sqlmock.AnyArg(), "membership-1", "user-1", "app-1").
		WillReturnRows(sqlmock.NewRows([]string{"version_sum", "node_count"}).AddRow(0, 0))
	mock.ExpectQuery(`FROM navigations n WHERE n.application_id=\?.*n.status='active'.*n.visible=true`).
		WithArgs("app-1", maxTreeNodes+1).
		WillReturnRows(sqlmock.NewRows([]string{}))
	service := &Service{db: db}
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "membership-1"})
	tree, err := service.CurrentTree(ctx, "app-1")
	require.NoError(t, err)
	require.Empty(t, tree)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCurrentTreeCachesOnlyRevisionedSourceRecords(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	expectRevision := func() {
		mock.ExpectQuery(`FROM applications a JOIN tenant_application_grants g`).
			WithArgs("tenant-1", sqlmock.AnyArg(), sqlmock.AnyArg(), "membership-1", "user-1", "app-1").
			WillReturnRows(sqlmock.NewRows([]string{"version_sum", "node_count"}).AddRow(1, 1))
	}
	expectRevision()
	rows := sqlmock.NewRows([]string{"id", "application_id", "parent_id", "navigation_key", "name", "navigation_type", "route_path", "component", "icon", "resource", "action", "visible", "status", "sort_order", "metadata", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow("nav-1", "app-1", nil, "home", "Home", "menu", "/home", "home/index", "", "", "", true, "active", 1, []byte(`{}`), time.Now(), "system", time.Now(), "system", 1)
	mock.ExpectQuery(`FROM navigations n WHERE n.application_id=\?`).WithArgs("app-1", maxTreeNodes+1).WillReturnRows(rows)
	expectRevision()
	store := &memoryCache{values: make(map[string][]byte)}
	service := &Service{db: db, cache: store, cacheTTL: time.Minute}
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "membership-1"})

	first, err := service.CurrentTree(ctx, "app-1")
	require.NoError(t, err)
	second, err := service.CurrentTree(ctx, "app-1")
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Equal(t, first, second)
	require.Equal(t, 1, store.sets)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCurrentSourceCacheKeyChangesWithRevision(t *testing.T) {
	t.Parallel()
	base := currentSourceCacheKey("app-1", 1, 1)
	require.NotEqual(t, base, currentSourceCacheKey("app-1", 2, 1))
	require.NotEqual(t, base, currentSourceCacheKey("app-1", 1, 2))
}

func TestFilterKeepsAncestorsAndExcludesUnmatchedBranches(t *testing.T) {
	t.Parallel()
	root := "root"
	records := []Record{
		{ID: root, ApplicationID: "app-1", Key: "system", Name: "System", Type: "directory", Status: "active"},
		{ID: "members", ApplicationID: "app-1", ParentID: &root, Key: "members", Name: "Members", Type: "menu", Status: "active"},
		{ID: "settings", ApplicationID: "app-1", ParentID: &root, Key: "settings", Name: "Settings", Type: "menu", Status: "disabled"},
	}
	filtered := filter(records, TreeInput{Keyword: "member", Statuses: []string{"active"}})
	require.Equal(t, []string{"root", "members"}, []string{filtered[0].ID, filtered[1].ID})
}
