package httptransport

import (
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	"github.com/stretchr/testify/require"
)

func TestCatalogPageRequestMapsResourceFilters(t *testing.T) {
	t.Parallel()
	request := CatalogPageRequest{
		Page:                2,
		PageSize:            20,
		ResourceKey:         "tenant.member",
		Scope:               resourceaction.ResourceScopeTenant,
		ActiveResourcesOnly: true,
	}
	input := request.input()
	require.Equal(t, 2, input.Page)
	require.Equal(t, 20, input.PageSize)
	require.Equal(t, "tenant.member", input.ResourceKey)
	require.Equal(t, resourceaction.ResourceScopeTenant, input.Scope)
	require.True(t, input.ActiveResourcesOnly)
}
