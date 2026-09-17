package navigation

import (
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/stretchr/testify/require"
)

func TestValidateNavigationTypeAndAuthorizationTarget(t *testing.T) {
	registry, err := pbac.NewRegistryFromDefinitions(pbac.ResourceDefinitions{{Key: "tenant.member", Name: "member", Scope: pbac.ResourceScopeTenant, Actions: []pbac.ActionDefinition{{Key: "list", Name: "list"}}}})
	require.NoError(t, err)
	require.NoError(t, Validate(Input{ApplicationID: "app-1", Key: "members", Name: "Members", Type: "menu", RoutePath: "/members", Component: "system/members", Resource: "tenant.member", Action: "list", Status: "active"}, registry))
	require.ErrorIs(t, Validate(Input{ApplicationID: "app-1", Key: "members", Name: "Members", Type: "menu", RoutePath: "/members", Component: "system/members", Status: "active"}, registry), ErrInvalid)
	require.ErrorIs(t, Validate(Input{ApplicationID: "app-1", Key: "system", Name: "System", Type: "directory", Resource: "tenant.member", Action: "list", Status: "active"}, registry), ErrInvalid)
}

func TestBuildNavigationTree(t *testing.T) {
	parent := "root"
	tree, err := Build([]Record{{ID: "root", ApplicationID: "app-1", Type: "directory"}, {ID: "members", ApplicationID: "app-1", ParentID: &parent, Type: "menu"}})
	require.NoError(t, err)
	require.Len(t, tree, 1)
	require.Len(t, tree[0].Children, 1)
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
		{ID: parent, ApplicationID: "app-1", Type: "directory", Visible: true, Status: "active"},
		{ID: "allowed", ApplicationID: "app-1", ParentID: &parent, Type: "menu", Visible: true, Status: "active"},
		{ID: "denied", ApplicationID: "app-1", ParentID: &parent, Type: "menu", Visible: true, Status: "active"},
	})
	require.NoError(t, err)
	visible := PruneVisible(tree, map[string]struct{}{"allowed": {}})
	require.Len(t, visible, 1)
	require.Equal(t, "allowed", visible[0].Children[0].ID)
	require.Len(t, visible[0].Children, 1)
	require.Len(t, tree[0].Children, 2, "input tree must not be mutated")
}
