package navigation

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
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

func TestCurrentTreeScopesGrantAndMembershipInSQL(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery(`FROM navigations n JOIN applications a.*JOIN tenant_application_grants g.*g.tenant_id=\?.*JOIN tenant_memberships m.*n.application_id=\?`).
		WithArgs("tenant-1", sqlmock.AnyArg(), sqlmock.AnyArg(), "membership-1", "user-1", "app-1", maxTreeNodes+1).
		WillReturnRows(sqlmock.NewRows([]string{}))
	service := &Service{db: db}
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "membership-1"})
	tree, err := service.CurrentTree(ctx, "app-1")
	require.NoError(t, err)
	require.Empty(t, tree)
	require.NoError(t, mock.ExpectationsWereMet())
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
