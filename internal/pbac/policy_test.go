package pbac

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	registry, err := NewRegistry([]ResourceDefinition{
		{Key: "member", Name: "Member", Scope: ResourceScopeTenant, Actions: []ActionDefinition{{Key: "read", Name: "Read"}, {Key: "update", Name: "Update"}}},
		{Key: "platform.user", Name: "Platform user", Scope: ResourceScopePlatform, Actions: []ActionDefinition{{Key: "update", Name: "Update"}}},
		{Key: "identity.profile", Name: "Current profile", Scope: ResourceScopePrincipal, Actions: []ActionDefinition{{Key: "read", Name: "Read"}}},
	})
	require.NoError(t, err)
	return registry
}

func TestParseAndValidatePolicy(t *testing.T) {
	t.Parallel()
	document := `
api_version: authorization.platform/v1
kind: ActionPolicy
metadata:
  code: department-manager-maintain-member
  name: Department manager maintains members
scope:
  type: tenant
  tenant_id: tenant-001
spec:
  subject:
    authenticated: true
    role: department_manager
  resource:
    type: member
  actions: [read, update]
  effect: allow
`
	policy, err := ParsePolicy([]byte(document))
	require.NoError(t, err)
	require.Empty(t, policy.Spec.Subject.Role)
	require.Equal(t, []string{"department_manager"}, policy.Spec.Subject.Roles.AnyOf)
	require.NoError(t, policy.Validate(testRegistry(t)))

	_, err = ParsePolicy([]byte(document + "  condition: resource.owner_id == subject.id\n"))
	require.ErrorIs(t, err, ErrInvalidPolicy, "operation policies must reject data conditions")
}

func TestParsePolicyRejectsUnknownFieldsAndMultipleDocuments(t *testing.T) {
	t.Parallel()
	_, err := ParsePolicy([]byte("api_version: authorization.platform/v1\nkind: ActionPolicy\nunknown: true\n"))
	require.ErrorIs(t, err, ErrInvalidPolicy)

	_, err = ParsePolicy([]byte("api_version: authorization.platform/v1\nkind: ActionPolicy\n---\napi_version: authorization.platform/v1\n"))
	require.ErrorIs(t, err, ErrInvalidPolicy)
}

func TestPolicyValidation(t *testing.T) {
	t.Parallel()
	authenticated := true
	base := Policy{
		APIVersion: APIVersionV1,
		Kind:       KindPolicy,
		Metadata:   PolicyMetadata{Code: "member-update", Name: "Member update"},
		Scope:      PolicyScope{Type: PolicyScopeTenant, TenantID: "tenant-001"},
		Spec: PolicySpec{
			Subject:  SubjectMatcher{Authenticated: &authenticated, Roles: RolesMatcher{AnyOf: []string{"department_manager"}}},
			Resource: ResourceMatcher{Type: "member"},
			Actions:  []string{"update"},
			Effect:   EffectAllow,
		},
	}
	tests := []struct {
		name   string
		mutate func(*Policy)
	}{
		{name: "unknown api version", mutate: func(p *Policy) { p.APIVersion = "pbac.platform/v2" }},
		{name: "global scope with tenant", mutate: func(p *Policy) { p.Scope.Type = PolicyScopeGlobal }},
		{name: "tenant missing tenant id", mutate: func(p *Policy) { p.Scope.TenantID = "" }},
		{name: "duplicate action", mutate: func(p *Policy) { p.Spec.Actions = []string{"update", "update"} }},
		{name: "unknown action", mutate: func(p *Policy) { p.Spec.Actions = []string{"delete"} }},
		{name: "unknown resource", mutate: func(p *Policy) { p.Spec.Resource.Type = "unknown" }},
		{name: "invalid effect", mutate: func(p *Policy) { p.Spec.Effect = "permit" }},
		{name: "empty matcher list", mutate: func(p *Policy) { p.Spec.Subject.Types = []string{} }},
		{name: "conflicting role", mutate: func(p *Policy) { p.Spec.Subject.Roles.NoneOf = []string{"department_manager"} }},
		{name: "tenant policy targets platform resource", mutate: func(p *Policy) { p.Spec.Resource.Type = "platform.user" }},
		{name: "tenant policy targets principal resource", mutate: func(p *Policy) { p.Spec.Resource.Type, p.Spec.Actions = "identity.profile", []string{"read"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			policy := base
			policy.Spec.Actions = append([]string(nil), base.Spec.Actions...)
			policy.Spec.Subject.Roles.AnyOf = append([]string(nil), base.Spec.Subject.Roles.AnyOf...)
			test.mutate(&policy)
			require.ErrorIs(t, policy.Validate(testRegistry(t)), ErrInvalidPolicy)
		})
	}
}

func TestGlobalPolicyMayTargetPlatformResource(t *testing.T) {
	t.Parallel()
	policy := Policy{
		APIVersion: APIVersionV1,
		Kind:       KindPolicy,
		Metadata:   PolicyMetadata{Code: "platform-user-update", Name: "Update platform user"},
		Scope:      PolicyScope{Type: PolicyScopeGlobal},
		Spec: PolicySpec{
			Resource: ResourceMatcher{Type: "platform.user"},
			Actions:  []string{"update"},
			Effect:   EffectAllow,
		},
	}
	require.NoError(t, policy.Validate(testRegistry(t)))
}
