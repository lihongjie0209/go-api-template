package httptransport

import (
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/navigation"
	"github.com/stretchr/testify/require"
)

func TestCollectNavigationCapabilitiesUsesOnlyActiveVisibleMenus(t *testing.T) {
	t.Parallel()
	nodes := []*navigation.Node{{
		Record: navigation.Record{ID: "root", Type: "directory", Visible: true, Status: "active"},
		Children: []*navigation.Node{
			{Record: navigation.Record{ID: "allowed", Type: "menu", Resource: "tenant.member", Action: "list", Visible: true, Status: "active"}, Children: []*navigation.Node{}},
			{Record: navigation.Record{ID: "hidden", Type: "menu", Resource: "tenant.member", Action: "list", Visible: false, Status: "active"}, Children: []*navigation.Node{}},
		},
	}}
	requests := []authorization.CapabilityRequest{}
	collectNavigationCapabilities(nodes, &requests)
	require.Equal(t, []authorization.CapabilityRequest{{Key: "allowed", Resource: "tenant.member", Action: "list"}}, requests)
}
