package navigation

import (
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	"github.com/stretchr/testify/require"
)

func TestValidateNavigationTypeAndAuthorizationTarget(t *testing.T) {
	registry, err := resourceaction.NewRegistryFromDefinitions(resourceaction.ResourceDefinitions{{Key: "tenant.member", Name: "member", Scope: resourceaction.ResourceScopeTenant, Actions: []resourceaction.ActionDefinition{{Key: "list", Name: "list"}}}})
	require.NoError(t, err)
	require.NoError(t, Validate(Input{ApplicationID: "app-1", Key: "members", Name: "Members", Type: "menu", RoutePath: "/members", Component: "system/members", Resource: "tenant.member", Action: "list", Status: "active"}, registry))
	require.ErrorIs(t, Validate(Input{ApplicationID: "app-1", Key: "members", Name: "Members", Type: "menu", RoutePath: "/members", Component: "system/members", Status: "active"}, registry), ErrInvalid)
	require.ErrorIs(t, Validate(Input{ApplicationID: "app-1", Key: "system", Name: "System", Type: "directory", Resource: "tenant.member", Action: "list", Status: "active"}, registry), ErrInvalid)
}

func TestValidateNavigationRoutePathRejectsDotSegments(t *testing.T) {
	t.Parallel()
	registry, err := resourceaction.NewRegistryFromDefinitions(resourceaction.ResourceDefinitions{{Key: "tenant.member", Name: "member", Scope: resourceaction.ResourceScopeTenant, Actions: []resourceaction.ActionDefinition{{Key: "list", Name: "list"}}}})
	require.NoError(t, err)
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "ordinary nested route", path: "/reports/monthly"},
		{name: "dotted semantic segment", path: "/.well-known/status"},
		{name: "root path has no route segment", path: "/", wantErr: true},
		{name: "vue router parameter", path: "/reports/:period", wantErr: true},
		{name: "root dot segment", path: "/./reports", wantErr: true},
		{name: "nested dot segment", path: "/reports/./monthly", wantErr: true},
		{name: "parent segment", path: "/reports/../monthly", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(Input{
				ApplicationID: "app-1",
				Key:           "reports",
				Name:          "Reports",
				Type:          "menu",
				RoutePath:     tt.path,
				Component:     "platform/runtime/index",
				Resource:      "tenant.member",
				Action:        "list",
				Status:        "active",
			}, registry)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalid)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateDirectoryKeyWithRouteParameterSyntax(t *testing.T) {
	t.Parallel()
	input := Input{
		ApplicationID: "app-1",
		Key:           "reports:monthly",
		Name:          "Reports",
		Type:          "directory",
		Status:        "active",
	}
	require.ErrorIs(t, Validate(input, nil), ErrInvalid)
	input.RoutePath = "/reports-monthly"
	require.NoError(t, Validate(input, nil))
	input.RoutePath = "/"
	require.ErrorIs(t, Validate(input, nil), ErrInvalid)
}

func TestBuildRejectsHistoricalParameterRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		record Record
	}{
		{name: "explicit path", record: Record{ID: "menu", ApplicationID: "app-1", Key: "menu", Type: "menu", RoutePath: "/reports/:period"}},
		{name: "root path", record: Record{ID: "menu", ApplicationID: "app-1", Key: "menu", Type: "menu", RoutePath: "/"}},
		{name: "directory root path", record: Record{ID: "directory", ApplicationID: "app-1", Key: "directory", Type: "directory", RoutePath: "/"}},
		{name: "directory key fallback", record: Record{ID: "directory", ApplicationID: "app-1", Key: "reports:monthly", Type: "directory"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Build([]Record{tt.record})
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestBuildNavigationTree(t *testing.T) {
	parent := "root"
	tree, err := Build([]Record{{ID: "root", ApplicationID: "app-1", Key: "root", Name: "System", Type: "directory"}, {ID: "members", ApplicationID: "app-1", ParentID: &parent, Key: "members", Name: "Members", Type: "menu", RoutePath: "/members"}})
	require.NoError(t, err)
	require.Len(t, tree, 1)
	require.Len(t, tree[0].Children, 1)
	require.Equal(t, "System", tree[0].Children[0].ParentName)
	require.Empty(t, tree[0].ParentName)
	require.Empty(t, tree[0].Children[0].Children)
}

func TestBuildRejectsCrossApplicationAndMenuParent(t *testing.T) {
	parent := "root"
	_, err := Build([]Record{{ID: "root", ApplicationID: "app-1", Type: "directory"}, {ID: "members", ApplicationID: "app-2", ParentID: &parent, Type: "menu"}})
	require.ErrorIs(t, err, ErrInvalid)
	_, err = Build([]Record{{ID: "root", ApplicationID: "app-1", Type: "menu"}, {ID: "members", ApplicationID: "app-1", ParentID: &parent, Type: "menu"}})
	require.ErrorIs(t, err, ErrInvalid)
}

func TestPruneVisibleKeepsOnlyAllowedMenusAndAncestors(t *testing.T) {
	t.Parallel()
	parent := "root"
	tree, err := Build([]Record{
		{ID: parent, ApplicationID: "app-1", Key: "root", Type: "directory", Visible: true, Status: "active"},
		{ID: "allowed", ApplicationID: "app-1", ParentID: &parent, Key: "allowed", Type: "menu", RoutePath: "/allowed", Visible: true, Status: "active"},
		{ID: "denied", ApplicationID: "app-1", ParentID: &parent, Key: "denied", Type: "menu", RoutePath: "/denied", Visible: true, Status: "active"},
	})
	require.NoError(t, err)
	visible := PruneVisible(tree, map[string]struct{}{"allowed": {}})
	require.Len(t, visible, 1)
	require.Equal(t, "allowed", visible[0].Children[0].ID)
	require.Len(t, visible[0].Children, 1)
	require.Len(t, tree[0].Children, 2, "input tree must not be mutated")
}

func TestBuildRejectsDuplicateResolvedRoutePaths(t *testing.T) {
	t.Parallel()
	root := "root"
	tests := []struct {
		name    string
		records []Record
		wantErr bool
	}{
		{
			name: "sibling menus with the same route path",
			records: []Record{
				{ID: "one", ApplicationID: "app-1", Key: "one", Type: "menu", RoutePath: "/reports"},
				{ID: "two", ApplicationID: "app-1", Key: "two", Type: "menu", RoutePath: "/reports"},
			},
			wantErr: true,
		},
		{
			name: "nested menu collides with root menu",
			records: []Record{
				{ID: root, ApplicationID: "app-1", Key: "reports-root", Type: "directory", RoutePath: "/reports"},
				{ID: "nested", ApplicationID: "app-1", ParentID: &root, Key: "daily", Type: "menu", RoutePath: "/daily"},
				{ID: "root-menu", ApplicationID: "app-1", Key: "root-daily", Type: "menu", RoutePath: "/reports/daily"},
			},
			wantErr: true,
		},
		{
			name: "distinct paths under different directories",
			records: []Record{
				{ID: "left", ApplicationID: "app-1", Key: "left", Type: "directory", RoutePath: "/left"},
				{ID: "right", ApplicationID: "app-1", Key: "right", Type: "directory", RoutePath: "/right"},
				{ID: "left-child", ApplicationID: "app-1", ParentID: stringPointer("left"), Key: "daily-left", Type: "menu", RoutePath: "/daily"},
				{ID: "right-child", ApplicationID: "app-1", ParentID: stringPointer("right"), Key: "daily-right", Type: "menu", RoutePath: "/daily"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Build(tt.records)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrConflict)
				return
			}
			require.NoError(t, err)
		})
	}
}

func stringPointer(value string) *string { return &value }
