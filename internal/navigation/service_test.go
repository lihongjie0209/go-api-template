package navigation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
