package pbac

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSimulatorUsesIsolatedDenyOverridesEvaluation(t *testing.T) {
	registry, err := NewRegistryFromDefinitions(ResourceDefinitions{resource("tenant.member", "member", ResourceScopeTenant, "update")})
	require.NoError(t, err)
	authenticated := true
	result, err := NewSimulator(registry).Simulate(t.Context(), SimulationInput{
		Policy:  Policy{APIVersion: APIVersionV1, Kind: KindPolicy, Metadata: PolicyMetadata{Code: "deny-member-update", Name: "Deny member update"}, Scope: PolicyScope{Type: PolicyScopeTenant, TenantID: "tenant-1"}, Spec: PolicySpec{Subject: SubjectMatcher{Authenticated: &authenticated}, Resource: ResourceMatcher{Type: "tenant.member"}, Actions: []string{"update"}, Effect: EffectDeny}},
		Request: EvaluationRequest{Subject: Subject{ID: "user-1", Authenticated: true, TenantID: "tenant-1"}, Resource: Resource{Type: "tenant.member", TenantID: "tenant-1"}, Action: "update"},
	})
	require.NoError(t, err)
	require.Equal(t, DecisionEffectDeny, result.Effect)
	require.Equal(t, ReasonExplicitDeny, result.ReasonCode)
}

func TestSimulatorRejectsTargetOutsideCandidatePolicy(t *testing.T) {
	registry, err := NewRegistryFromDefinitions(ResourceDefinitions{
		resource("tenant.member", "member", ResourceScopeTenant, "read", "update"),
		resource("tenant.department", "department", ResourceScopeTenant, "update"),
	})
	require.NoError(t, err)
	authenticated := true
	policy := Policy{APIVersion: APIVersionV1, Kind: KindPolicy, Metadata: PolicyMetadata{Code: "allow-member-update", Name: "Allow member update"}, Scope: PolicyScope{Type: PolicyScopeTenant, TenantID: "tenant-1"}, Spec: PolicySpec{Subject: SubjectMatcher{Authenticated: &authenticated}, Resource: ResourceMatcher{Type: "tenant.member"}, Actions: []string{"update"}, Effect: EffectAllow}}
	tests := []struct {
		name     string
		action   string
		resource Resource
	}{
		{name: "action mismatch", action: "read", resource: Resource{Type: "tenant.member", TenantID: "tenant-1"}},
		{name: "resource mismatch", action: "update", resource: Resource{Type: "tenant.department", TenantID: "tenant-1"}},
		{name: "resource tenant mismatch", action: "update", resource: Resource{Type: "tenant.member", TenantID: "tenant-2"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewSimulator(registry).Simulate(t.Context(), SimulationInput{
				Policy:  policy,
				Request: EvaluationRequest{Subject: Subject{ID: "user-1", Authenticated: true, TenantID: "tenant-1"}, Resource: test.resource, Action: test.action},
			})
			require.ErrorIs(t, err, ErrInvalidSimulation)
		})
	}
}

func TestSimulatorReturnsCanceledContext(t *testing.T) {
	registry, err := NewRegistryFromDefinitions(ResourceDefinitions{resource("tenant.member", "member", ResourceScopeTenant, "update")})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = NewSimulator(registry).Simulate(ctx, SimulationInput{})
	require.ErrorIs(t, err, context.Canceled)
}
