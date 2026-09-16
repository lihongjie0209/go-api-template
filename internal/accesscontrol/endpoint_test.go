package accesscontrol

import (
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/stretchr/testify/require"
)

func endpointResourceRegistry(t *testing.T) *pbac.Registry {
	t.Helper()
	registry, err := pbac.NewRegistry([]pbac.ResourceDefinition{{
		Key: "tenant.member", Name: "Tenant member", Scope: pbac.ResourceScopeTenant,
		Actions: []pbac.ActionDefinition{{Key: "list", Name: "List"}, {Key: "update", Name: "Update"}},
	}})
	require.NoError(t, err)
	return registry
}

func TestNewEndpointRegistryRequiresExplicitAuthorizationMetadata(t *testing.T) {
	t.Parallel()
	registry, err := NewEndpointRegistry(endpointResourceRegistry(t), []Endpoint{
		{Transport: TransportHTTP, Operation: "POST /api/v1/tenant-members/page", Authentication: AuthenticationJWT, Resource: "tenant.member", Action: "list", DataPermission: DataPermissionRequired},
		{Transport: TransportHTTP, Operation: "POST /api/v1/auth/login", Authentication: AuthenticationPublic, DataPermission: DataPermissionNone},
		{Transport: TransportGRPC, Operation: "/platform.member.v1.MemberService/Update", Authentication: AuthenticationService, Resource: "tenant.member", Action: "update", DataPermission: DataPermissionRequired},
	})
	require.NoError(t, err)

	endpoint, ok := registry.Lookup(TransportHTTP, "POST /api/v1/tenant-members/page")
	require.True(t, ok)
	require.Equal(t, "tenant.member", endpoint.Resource)
	require.Equal(t, "list", endpoint.Action)
	require.Equal(t, DataPermissionRequired, endpoint.DataPermission)
}

func TestNewEndpointRegistryRejectsUnsafeDescriptors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		endpoints []Endpoint
		target    error
	}{
		{name: "protected endpoint missing resource", endpoints: []Endpoint{{Transport: TransportHTTP, Operation: "POST /api/v1/members/page", Authentication: AuthenticationJWT}}, target: ErrInvalidEndpoint},
		{name: "public endpoint declares resource", endpoints: []Endpoint{{Transport: TransportHTTP, Operation: "POST /api/v1/public/members", Authentication: AuthenticationPublic, Resource: "tenant.member", Action: "list"}}, target: ErrInvalidEndpoint},
		{name: "public endpoint requires data permission", endpoints: []Endpoint{{Transport: TransportHTTP, Operation: "POST /api/v1/public/members", Authentication: AuthenticationPublic, DataPermission: DataPermissionRequired}}, target: ErrInvalidEndpoint},
		{name: "protected endpoint lacks data permission justification", endpoints: []Endpoint{{Transport: TransportHTTP, Operation: "POST /api/v1/members/page", Authentication: AuthenticationJWT, Resource: "tenant.member", Action: "list", DataPermission: DataPermissionNone}}, target: ErrInvalidEndpoint},
		{name: "unknown action", endpoints: []Endpoint{{Transport: TransportHTTP, Operation: "POST /api/v1/members/delete", Authentication: AuthenticationJWT, Resource: "tenant.member", Action: "delete", DataPermission: DataPermissionNone}}, target: pbac.ErrUnknownAction},
		{name: "duplicate operation", endpoints: []Endpoint{{Transport: TransportHTTP, Operation: "POST /api/v1/members/page", Authentication: AuthenticationJWT, Resource: "tenant.member", Action: "list", DataPermission: DataPermissionNone, DataPermissionReason: "tenant-wide operation"}, {Transport: TransportHTTP, Operation: "POST /api/v1/members/page", Authentication: AuthenticationJWT, Resource: "tenant.member", Action: "list", DataPermission: DataPermissionNone, DataPermissionReason: "tenant-wide operation"}}, target: ErrDuplicateEndpoint},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewEndpointRegistry(endpointResourceRegistry(t), test.endpoints)
			require.ErrorIs(t, err, test.target)
		})
	}
}

func TestEndpointRegistryValidatesExactRuntimeCoverage(t *testing.T) {
	t.Parallel()
	registry, err := NewEndpointRegistry(endpointResourceRegistry(t), []Endpoint{
		{Transport: TransportHTTP, Operation: "POST /api/v1/members/page", Authentication: AuthenticationJWT, Resource: "tenant.member", Action: "list", DataPermission: DataPermissionRequired},
	})
	require.NoError(t, err)

	require.NoError(t, registry.ValidateCoverage([]Operation{{Transport: TransportHTTP, Name: "POST /api/v1/members/page"}}))
	require.ErrorIs(t, registry.ValidateCoverage([]Operation{{Transport: TransportHTTP, Name: "POST /api/v1/members/get"}}), ErrEndpointCoverage)
	require.ErrorIs(t, registry.ValidateCoverage([]Operation{
		{Transport: TransportHTTP, Name: "POST /api/v1/members/page"},
		{Transport: TransportHTTP, Name: "POST /api/v1/members/get"},
	}), ErrEndpointCoverage)
}
