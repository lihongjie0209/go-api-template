package pbac

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseResourceAction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		input       string
		expected    ResourceAction
		expectedErr bool
	}{
		{name: "simple", input: "member:update", expected: ResourceAction{Resource: "member", Action: "update"}},
		{name: "nested resource", input: "identity.member:assign-role", expected: ResourceAction{Resource: "identity.member", Action: "assign-role"}},
		{name: "missing separator", input: "member.update", expectedErr: true},
		{name: "multiple separators", input: "member:update:all", expectedErr: true},
		{name: "uppercase resource", input: "Member:update", expectedErr: true},
		{name: "surrounding whitespace", input: " member:update", expectedErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actual, err := ParseResourceAction(test.input)
			if test.expectedErr {
				require.ErrorIs(t, err, ErrInvalidResourceAction)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.expected, actual)
			require.Equal(t, test.input, actual.String())
		})
	}
}

func TestRegistry(t *testing.T) {
	t.Parallel()
	definitions := []ResourceDefinition{
		{Key: "member", Name: "Member", Scope: ResourceScopeTenant, Actions: []ActionDefinition{{Key: "read", Name: "Read"}, {Key: "update", Name: "Update"}}},
		{Key: "platform.user", Name: "Platform user", Scope: ResourceScopePlatform, Actions: []ActionDefinition{{Key: "disable", Name: "Disable"}}},
	}
	registry, err := NewRegistry(definitions)
	require.NoError(t, err)

	resource, action, err := registry.Resolve("member", "update")
	require.NoError(t, err)
	require.Equal(t, ResourceScopeTenant, resource.Scope)
	require.Equal(t, "Update", action.Name)

	_, _, err = registry.Resolve("missing", "read")
	require.ErrorIs(t, err, ErrUnknownResource)
	_, _, err = registry.Resolve("member", "missing")
	require.ErrorIs(t, err, ErrUnknownAction)

	returned := registry.Definitions()
	require.Equal(t, "member", returned[0].Key)
	returned[0].Actions[0].Key = "mutated"
	_, _, err = registry.Resolve("member", "read")
	require.NoError(t, err, "returned definitions must not mutate the registry")
}

func TestRegistryRejectsDuplicates(t *testing.T) {
	t.Parallel()
	_, err := NewRegistry([]ResourceDefinition{
		{Key: "member", Name: "Member", Scope: ResourceScopeTenant, Actions: []ActionDefinition{{Key: "read", Name: "Read"}}},
		{Key: "member", Name: "Member again", Scope: ResourceScopeTenant, Actions: []ActionDefinition{{Key: "update", Name: "Update"}}},
	})
	require.True(t, errors.Is(err, ErrDuplicateResource))

	_, err = NewRegistry([]ResourceDefinition{{
		Key: "member", Name: "Member", Scope: ResourceScopeTenant,
		Actions: []ActionDefinition{{Key: "read", Name: "Read"}, {Key: "read", Name: "Read again"}},
	}})
	require.True(t, errors.Is(err, ErrDuplicateAction))
}

func TestPlatformResourceDefinitions(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistryFromDefinitions(PlatformResourceDefinitions())
	require.NoError(t, err)
	require.Len(t, registry.Definitions(), 32)

	tests := []struct {
		name     string
		resource string
		action   string
		scope    ResourceScope
	}{
		{name: "platform user", resource: "identity.user", action: "reset-password", scope: ResourceScopePlatform},
		{name: "principal session", resource: "identity.session", action: "revoke", scope: ResourceScopePrincipal},
		{name: "self capability", resource: "authorization.capability", action: "evaluate", scope: ResourceScopePrincipal},
		{name: "tenant member", resource: "tenant.member", action: "assign-role", scope: ResourceScopeTenant},
		{name: "tenant profile", resource: "tenant.profile", action: "update", scope: ResourceScopeTenant},
		{name: "current tenant navigation", resource: "navigation.current", action: "read", scope: ResourceScopeTenant},
		{name: "application", resource: "application", action: "update", scope: ResourceScopePlatform},
		{name: "tenant application grant", resource: "tenant.application-grant", action: "grant", scope: ResourceScopePlatform},
		{name: "current applications", resource: "application.current", action: "list", scope: ResourceScopeTenant},
		{name: "navigation", resource: "navigation", action: "create", scope: ResourceScopePlatform},
		{name: "tenant selection", resource: "tenant.selection", action: "switch", scope: ResourceScopePrincipal},
		{name: "dictionary item", resource: "dictionary.item", action: "list", scope: ResourceScopePlatform},
		{name: "tenant policy", resource: "pbac.tenant-policy", action: "publish", scope: ResourceScopeTenant},
		{name: "global data policy", resource: "data-permission.global-policy", action: "publish", scope: ResourceScopePlatform},
		{name: "tenant data policy", resource: "data-permission.tenant-policy", action: "set-status", scope: ResourceScopeTenant},
		{name: "internal authentication", resource: "identity.internal-authentication", action: "validate-session", scope: ResourceScopePlatform},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			resource, _, resolveErr := registry.Resolve(test.resource, test.action)
			require.NoError(t, resolveErr)
			require.Equal(t, test.scope, resource.Scope)
		})
	}
}

func TestNewRegistryFromGroupsRejectsCrossModuleDuplicates(t *testing.T) {
	t.Parallel()
	_, err := NewRegistryFromGroups([]ResourceDefinitions{
		{{Key: "tenant.member", Name: "Member", Scope: ResourceScopeTenant, Actions: []ActionDefinition{{Key: "read", Name: "Read"}}}},
		{{Key: "tenant.member", Name: "Duplicate", Scope: ResourceScopeTenant, Actions: []ActionDefinition{{Key: "list", Name: "List"}}}},
	})
	require.ErrorIs(t, err, ErrDuplicateResource)
}
