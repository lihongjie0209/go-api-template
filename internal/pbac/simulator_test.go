package pbac

import (
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
