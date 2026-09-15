package routepolicy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewRouteHasStableIdentity(t *testing.T) {
	t.Parallel()
	first, err := NewRoute("HTTP", "POST", "/api/v1/users/page", "identity-service", "v1")
	require.NoError(t, err)
	second, err := NewRoute("http", "post", "/api/v1/users/page", "identity-service", "v2")
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	require.Equal(t, "650787a3-70f7-56e6-94a2-da42f631168d", first.ID)
}

func TestNewRouteEscapesLeadingDotWithoutCollision(t *testing.T) {
	t.Parallel()
	dotRoute, err := NewRoute("http", "post", "/api/v1/.well-known/jwks.json", "identity-service", "v1")
	require.NoError(t, err)
	escapedRoute, err := NewRoute("http", "post", "/api/v1/x-.well-known/jwks.json", "identity-service", "v1")
	require.NoError(t, err)
	require.NotEqual(t, dotRoute.ID, escapedRoute.ID)

	dotRouteAgain, err := NewRoute("HTTP", "POST", "/api/v1/.well-known/jwks.json", "identity-service", "v2")
	require.NoError(t, err)
	require.Equal(t, dotRoute.ID, dotRouteAgain.ID)
}
