package datapermission

import (
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/stretchr/testify/require"
)

func TestSimulatorReturnsParameterizedPreviewWithoutValues(t *testing.T) {
	resources, err := pbac.NewRegistryFromDefinitions(pbac.ResourceDefinitions{{Key: "tenant.member", Name: "member", Scope: pbac.ResourceScopeTenant, Actions: []pbac.ActionDefinition{{Key: "update", Name: "update"}}}})
	require.NoError(t, err)
	schema, err := NewSchema("tenant.member", map[string]Field{"owner_id": {Column: "tm.user_id", Type: ValueTypeText}, "status": {Column: "tm.status", Type: ValueTypeText}})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	authenticated := true
	result, err := NewSimulator(schemas, resources).Simulate(t.Context(), SimulationInput{
		Policy:  Policy{APIVersion: PolicyAPIVersion, Kind: PolicyKind, Metadata: PolicyMetadata{Code: "member-owner", Name: "Member owner"}, Scope: PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"}, Spec: PolicySpec{Subject: pbac.SubjectMatcher{Authenticated: &authenticated}, Resource: "tenant.member", Actions: []string{"update"}, Condition: "resource.owner_id == subject.id", ProposedCondition: `proposed.status == "active"`, Effect: EffectAllow}},
		Action:  "update",
		Subject: pbac.Subject{ID: "user-1", Authenticated: true, TenantID: "tenant-1"}, SubjectAttributes: SubjectAttributes{"id": "user-1"},
		ResourceAttributes: ResourceAttributes{"owner_id": "user-1", "status": "active"}, ProposedAttributes: ResourceAttributes{"owner_id": "user-1", "status": "disabled"},
	})
	require.NoError(t, err)
	require.True(t, result.CurrentAllowed)
	require.False(t, result.TransitionAllowed)
	require.Equal(t, 1, result.SQL.ParameterCount)
	require.NotContains(t, result.SQL.Clause, "user-1")
}
