package datapermission

import (
	"context"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	"github.com/stretchr/testify/require"
)

func TestSimulatorReturnsParameterizedPreviewWithoutValues(t *testing.T) {
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.ResourceDefinitions{{Key: "tenant.member", Name: "member", Scope: resourceaction.ResourceScopeTenant, Actions: []resourceaction.ActionDefinition{{Key: "update", Name: "update"}}}})
	require.NoError(t, err)
	schema, err := NewSchema("tenant.member", map[string]Field{"owner_id": {Column: "tm.user_id", Type: ValueTypeText}, "status": {Column: "tm.status", Type: ValueTypeText}})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	runtime, err := NewRuntimeEngine(schemas, resources)
	require.NoError(t, err)
	authenticated := true
	result, err := NewSimulator(schemas, resources, runtime).Simulate(t.Context(), SimulationInput{
		Policy:  Policy{APIVersion: PolicyAPIVersion, Kind: PolicyKind, Metadata: PolicyMetadata{Code: "member-owner", Name: "Member owner"}, Scope: PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"}, Spec: PolicySpec{Subject: resourceaction.SubjectMatcher{Authenticated: &authenticated}, Resource: "tenant.member", Actions: []string{"update"}, Condition: "resource.owner_id == subject.id", ProposedCondition: `proposed.status == "active"`, Effect: EffectAllow}},
		Action:  "update",
		Subject: resourceaction.Subject{ID: "user-1", Authenticated: true, TenantID: "tenant-1"}, Resource: resourceaction.Resource{Type: "tenant.member", TenantID: "tenant-1"}, SubjectAttributes: SubjectAttributes{"id": "user-1"},
		ResourceAttributes: ResourceAttributes{"owner_id": "user-1", "status": "active"}, ProposedAttributes: ResourceAttributes{"owner_id": "user-1", "status": "disabled"},
	})
	require.NoError(t, err)
	require.False(t, result.Baseline.CurrentAllowed)
	require.True(t, result.Candidate.CurrentAllowed)
	require.False(t, result.Candidate.TransitionAllowed)
	require.Equal(t, 1, result.Candidate.SQL.ParameterCount)
	require.NotContains(t, result.Candidate.SQL.Clause, "user-1")
	require.True(t, result.Changed)
}

func TestSimulatorCombinesCandidateWithActiveDataPolicies(t *testing.T) {
	simulator, candidate := newDataPermissionSimulator(t)
	authenticated := true
	activeAllow := clonePolicy(candidate)
	activeAllow.Metadata.Code = "member-all"
	activeAllow.Metadata.Name = "All members"
	activeAllow.Spec.Condition = ""
	require.NoError(t, simulator.runtime.Replace([]Policy{activeAllow}))
	candidate.Spec.Effect = EffectDeny

	result, err := simulator.Simulate(t.Context(), SimulationInput{
		Policy: candidate, Action: "update",
		Subject:           resourceaction.Subject{ID: "user-1", Authenticated: authenticated, TenantID: "tenant-1"},
		Resource:          resourceaction.Resource{Type: "tenant.member", TenantID: "tenant-1"},
		SubjectAttributes: SubjectAttributes{"id": "user-1"}, ResourceAttributes: ResourceAttributes{"owner_id": "user-1"},
	})
	require.NoError(t, err)
	require.True(t, result.Baseline.CurrentAllowed)
	require.False(t, result.Candidate.CurrentAllowed)
	require.Contains(t, result.Candidate.SQL.Clause, "NOT")
	require.True(t, result.Changed)
}

func TestSimulatorReportsUnchangedForIdenticalActiveDataPolicy(t *testing.T) {
	simulator, policy := newDataPermissionSimulator(t)
	require.NoError(t, simulator.runtime.Replace([]Policy{policy}))

	result, err := simulator.Simulate(t.Context(), SimulationInput{
		Policy: policy, Action: "update",
		Subject:           resourceaction.Subject{ID: "user-1", Authenticated: true, TenantID: "tenant-1"},
		Resource:          resourceaction.Resource{Type: "tenant.member", TenantID: "tenant-1"},
		SubjectAttributes: SubjectAttributes{"id": "user-1"}, ResourceAttributes: ResourceAttributes{"owner_id": "user-1"},
	})
	require.NoError(t, err)
	require.False(t, result.Changed)
}

func TestEnginePoliciesReturnsDeepCopy(t *testing.T) {
	simulator, policy := newDataPermissionSimulator(t)
	require.NoError(t, simulator.runtime.Replace([]Policy{policy}))

	policies, err := simulator.runtime.Policies()
	require.NoError(t, err)
	*policies[0].Spec.Subject.Authenticated = false
	policies[0].Spec.Actions[0] = "changed"

	unchanged, err := simulator.runtime.Policies()
	require.NoError(t, err)
	require.True(t, *unchanged[0].Spec.Subject.Authenticated)
	require.Equal(t, []string{"update"}, unchanged[0].Spec.Actions)
}

func TestSimulatorRejectsTargetOutsideCandidatePolicy(t *testing.T) {
	simulator, policy := newDataPermissionSimulator(t)
	tests := []struct {
		name     string
		action   string
		resource resourceaction.Resource
	}{
		{name: "action mismatch", action: "read", resource: resourceaction.Resource{Type: "tenant.member", TenantID: "tenant-1"}},
		{name: "resource mismatch", action: "update", resource: resourceaction.Resource{Type: "tenant.department", TenantID: "tenant-1"}},
		{name: "resource tenant mismatch", action: "update", resource: resourceaction.Resource{Type: "tenant.member", TenantID: "tenant-2"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := simulator.Simulate(t.Context(), SimulationInput{
				Policy: policy, Action: test.action,
				Subject:  resourceaction.Subject{ID: "user-1", Authenticated: true, TenantID: "tenant-1"},
				Resource: test.resource, SubjectAttributes: SubjectAttributes{"id": "user-1"},
				ResourceAttributes: ResourceAttributes{"owner_id": "user-1"},
			})
			require.ErrorIs(t, err, ErrInvalidSimulation)
		})
	}
}

func TestSimulatorReturnsCanceledContext(t *testing.T) {
	simulator, policy := newDataPermissionSimulator(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := simulator.Simulate(ctx, SimulationInput{Policy: policy})
	require.ErrorIs(t, err, context.Canceled)
}

func TestSimulatorEvaluatesSavedPolicyVersion(t *testing.T) {
	simulator, policy := newDataPermissionSimulator(t)
	document, err := MarshalPolicy(policy)
	require.NoError(t, err)
	input := SavedSimulationInput{
		ExpectedScope: PolicyScopeTenant, ActorTenantID: "tenant-1", PolicyCode: "member-owner",
		Action:             "update",
		Subject:            resourceaction.Subject{ID: "user-1", Authenticated: true, TenantID: "tenant-1"},
		Resource:           resourceaction.Resource{Type: "tenant.member", TenantID: "tenant-1"},
		SubjectAttributes:  SubjectAttributes{"id": "user-1"},
		ResourceAttributes: ResourceAttributes{"owner_id": "user-1"},
	}
	version := VersionRecord{PolicyID: "policy-1", VersionNumber: 2, Document: string(document)}
	result, err := simulator.SimulateVersion(t.Context(), version, input)
	require.NoError(t, err)
	require.False(t, result.Baseline.CurrentAllowed)
	require.True(t, result.Candidate.CurrentAllowed)
	require.True(t, result.Changed)

	version.Document = "not: [valid"
	_, err = simulator.SimulateVersion(t.Context(), version, input)
	require.ErrorIs(t, err, ErrInvalidPolicy)
	version.Document = string(document)
	input.Resource.TenantID = "tenant-2"
	_, err = simulator.SimulateVersion(t.Context(), version, input)
	require.ErrorIs(t, err, ErrPolicyScope)
	input.Resource.TenantID = "tenant-1"
	input.ExpectedScope = PolicyScopeGlobal
	_, err = simulator.SimulateVersion(t.Context(), version, input)
	require.ErrorIs(t, err, ErrPolicyScope)
	input.ExpectedScope = PolicyScopeTenant
	input.PolicyCode = "another-policy"
	_, err = simulator.SimulateVersion(t.Context(), version, input)
	require.ErrorIs(t, err, ErrInvalidPolicy)
}

func newDataPermissionSimulator(t *testing.T) (*Simulator, Policy) {
	t.Helper()
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.ResourceDefinitions{
		{Key: "tenant.member", Name: "member", Scope: resourceaction.ResourceScopeTenant, Actions: []resourceaction.ActionDefinition{{Key: "update", Name: "update"}, {Key: "read", Name: "read"}}},
		{Key: "tenant.department", Name: "department", Scope: resourceaction.ResourceScopeTenant, Actions: []resourceaction.ActionDefinition{{Key: "update", Name: "update"}}},
	})
	require.NoError(t, err)
	schema, err := NewSchema("tenant.member", map[string]Field{"owner_id": {Column: "tm.user_id", Type: ValueTypeText}})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	runtime, err := NewRuntimeEngine(schemas, resources)
	require.NoError(t, err)
	authenticated := true
	policy := Policy{APIVersion: PolicyAPIVersion, Kind: PolicyKind, Metadata: PolicyMetadata{Code: "member-owner", Name: "Member owner"}, Scope: PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"}, Spec: PolicySpec{Subject: resourceaction.SubjectMatcher{Authenticated: &authenticated}, Resource: "tenant.member", Actions: []string{"update"}, Condition: "resource.owner_id == subject.id", Effect: EffectAllow}}
	return NewSimulator(schemas, resources, runtime), policy
}
