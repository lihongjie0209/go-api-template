package accesscontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/pbac"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type authorizerFunc func(context.Context, platformprincipal.Principal, platformauthz.Requirement) error

func (f authorizerFunc) Authorize(ctx context.Context, principal platformprincipal.Principal, requirement platformauthz.Requirement) error {
	return f(ctx, principal, requirement)
}

func TestEnforcerAuthorizesDeclaredOperation(t *testing.T) {
	resources, err := pbac.NewRegistry([]pbac.ResourceDefinition{{Key: "tenant.member", Name: "member", Scope: pbac.ResourceScopeTenant, Actions: []pbac.ActionDefinition{{Key: "read", Name: "read"}}}})
	require.NoError(t, err)
	registry, err := NewEndpointRegistry(resources, []Endpoint{{Transport: TransportHTTP, Operation: "POST /members/get", Authentication: AuthenticationJWT, Resource: "tenant.member", Action: "read", DataPermission: DataPermissionRequired}})
	require.NoError(t, err)
	called := false
	enforcer := NewEnforcer(registry, authorizerFunc(func(_ context.Context, principal platformprincipal.Principal, requirement platformauthz.Requirement) error {
		called = true
		require.Equal(t, "u1", principal.ID)
		require.Equal(t, platformauthz.ScopeTenant, requirement.Scope)
		return nil
	}))
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "u1", Type: platformprincipal.TypeUser})
	endpoint, err := enforcer.Authorize(ctx, TransportHTTP, "POST /members/get")
	require.NoError(t, err)
	require.True(t, called)
	require.Equal(t, pbac.ResourceScopeTenant, endpoint.Scope)
}

func TestEnforcerMapsPrincipalResourceScope(t *testing.T) {
	resources, err := pbac.NewRegistry([]pbac.ResourceDefinition{{Key: "identity.session", Name: "session", Scope: pbac.ResourceScopePrincipal, Actions: []pbac.ActionDefinition{{Key: "list", Name: "list"}}}})
	require.NoError(t, err)
	registry, err := NewEndpointRegistry(resources, []Endpoint{{Transport: TransportHTTP, Operation: "POST /sessions/page", Authentication: AuthenticationJWT, Resource: "identity.session", Action: "list", DataPermission: DataPermissionNone, DataPermissionReason: "service enforces subject ownership"}})
	require.NoError(t, err)
	enforcer := NewEnforcer(registry, authorizerFunc(func(_ context.Context, _ platformprincipal.Principal, requirement platformauthz.Requirement) error {
		require.Equal(t, platformauthz.ScopePrincipal, requirement.Scope)
		return nil
	}))
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "u1", Type: platformprincipal.TypeUser})
	endpoint, err := enforcer.Authorize(ctx, TransportHTTP, "POST /sessions/page")
	require.NoError(t, err)
	require.Equal(t, pbac.ResourceScopePrincipal, endpoint.Scope)
}

func TestEnforcerFailsClosed(t *testing.T) {
	resources, err := pbac.NewRegistry([]pbac.ResourceDefinition{{Key: "identity.user", Name: "user", Scope: pbac.ResourceScopePlatform, Actions: []pbac.ActionDefinition{{Key: "read", Name: "read"}}}})
	require.NoError(t, err)
	registry, err := NewEndpointRegistry(resources, []Endpoint{{Transport: TransportGRPC, Operation: "/identity.v1.IdentityService/GetUser", Authentication: AuthenticationJWT, Resource: "identity.user", Action: "read", DataPermission: DataPermissionRequired}})
	require.NoError(t, err)
	enforcer := NewEnforcer(registry, authorizerFunc(func(context.Context, platformprincipal.Principal, platformauthz.Requirement) error {
		return errors.New("must not be called")
	}))
	_, err = enforcer.Authorize(context.Background(), TransportGRPC, "/identity.v1.IdentityService/GetUser")
	require.ErrorIs(t, err, ErrAuthentication)
	_, err = enforcer.Authorize(context.Background(), TransportGRPC, "/unknown.Service/Call")
	require.ErrorIs(t, err, ErrEndpointMissing)
}

func TestEnforcerAllowsOnlyExplicitPublicOperation(t *testing.T) {
	resources, err := pbac.NewRegistry([]pbac.ResourceDefinition{{Key: "identity.user", Name: "user", Scope: pbac.ResourceScopePlatform, Actions: []pbac.ActionDefinition{{Key: "read", Name: "read"}}}})
	require.NoError(t, err)
	registry, err := NewEndpointRegistry(resources, []Endpoint{{Transport: TransportHTTP, Operation: "POST /version", Authentication: AuthenticationPublic, DataPermission: DataPermissionNone}})
	require.NoError(t, err)
	enforcer := NewEnforcer(registry, nil)
	_, err = enforcer.Authorize(context.Background(), TransportHTTP, "POST /version")
	require.NoError(t, err)
}

func TestEndpointContextRoundTrip(t *testing.T) {
	t.Parallel()
	want := Endpoint{Transport: TransportHTTP, Operation: "POST /members/page", Resource: "tenant.member", Action: "list", DataPermission: DataPermissionRequired}
	got, ok := EndpointFromContext(WithEndpoint(context.Background(), want))
	require.True(t, ok)
	require.Equal(t, want, got)
}
