package pbac

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func enginePolicy(code string, scope PolicyScope, subject SubjectMatcher, effect Effect) Policy {
	return Policy{
		APIVersion: APIVersionV1,
		Kind:       KindPolicy,
		Metadata:   PolicyMetadata{Code: code, Name: code},
		Scope:      scope,
		Spec: PolicySpec{
			Subject:  subject,
			Resource: ResourceMatcher{Type: "member"},
			Actions:  []string{"update"},
			Effect:   effect,
		},
	}
}

func newTestEngine(t *testing.T, policies ...Policy) *Engine {
	t.Helper()
	engine, err := NewEngine(testRegistry(t), policies)
	require.NoError(t, err)
	return engine
}

func TestEngineDenyOverridesAllow(t *testing.T) {
	t.Parallel()
	authenticated := true
	allow := enginePolicy(
		"allow-department-manager",
		PolicyScope{Type: PolicyScopeGlobal},
		SubjectMatcher{Authenticated: &authenticated, Roles: RolesMatcher{AnyOf: []string{"department_manager"}}},
		EffectAllow,
	)
	deny := enginePolicy(
		"deny-suspended-member",
		PolicyScope{Type: PolicyScopeGlobal},
		SubjectMatcher{Roles: RolesMatcher{AnyOf: []string{"suspended_member"}}},
		EffectDeny,
	)
	engine := newTestEngine(t, allow, deny)
	request := EvaluationRequest{
		Subject: Subject{
			Authenticated: true,
			Type:          "user",
			TenantID:      "tenant-1",
			Roles:         []string{"department_manager", "suspended_member"},
		},
		Resource: Resource{Type: "member", TenantID: "tenant-1"},
		Action:   "update",
	}
	decision, err := engine.Evaluate(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, DecisionEffectDeny, decision.Effect)
	require.Equal(t, ReasonExplicitDeny, decision.ReasonCode)
	require.Equal(t, "deny-suspended-member", decision.DeniedByPolicyID)
	require.Equal(t, []string{"allow-department-manager", "deny-suspended-member"}, decision.MatchedPolicies)
}

func TestEngineTenantIsolationAndDefaultDeny(t *testing.T) {
	t.Parallel()
	allow := enginePolicy(
		"tenant-manager",
		PolicyScope{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		SubjectMatcher{Roles: RolesMatcher{AnyOf: []string{"manager"}}},
		EffectAllow,
	)
	engine := newTestEngine(t, allow)

	request := EvaluationRequest{
		Subject:  Subject{TenantID: "tenant-2", Roles: []string{"manager"}},
		Resource: Resource{Type: "member", TenantID: "tenant-2"},
		Action:   "update",
	}
	decision, err := engine.Evaluate(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, DecisionEffectDeny, decision.Effect)
	require.Equal(t, ReasonNoMatchingPolicy, decision.ReasonCode)

	request.Subject.TenantID = "tenant-1"
	request.Resource.TenantID = "tenant-1"
	decision, err = engine.Evaluate(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, DecisionEffectAllow, decision.Effect)
	require.Equal(t, ReasonAllowed, decision.ReasonCode)
}

func TestEngineRejectsUnknownResourceAction(t *testing.T) {
	t.Parallel()
	engine := newTestEngine(t)
	decision, err := engine.Evaluate(t.Context(), EvaluationRequest{
		Subject:  Subject{TenantID: "tenant-1"},
		Resource: Resource{Type: "member", TenantID: "tenant-1"},
		Action:   "delete",
	})
	require.Error(t, err)
	require.Equal(t, DecisionEffectIndeterminate, decision.Effect)
	require.Equal(t, ReasonResourceActionUnknown, decision.ReasonCode)
}

func TestEngineReplaceIsAtomic(t *testing.T) {
	t.Parallel()
	allow := enginePolicy("allow-member", PolicyScope{Type: PolicyScopeGlobal}, SubjectMatcher{}, EffectAllow)
	engine := newTestEngine(t, allow)
	invalid := allow
	invalid.Metadata.Code = "invalid-policy"
	invalid.Spec.Resource.Type = "unknown"
	require.Error(t, engine.Replace([]Policy{invalid}))

	decision, err := engine.Evaluate(t.Context(), EvaluationRequest{
		Subject:  Subject{TenantID: "tenant-1"},
		Resource: Resource{Type: "member", TenantID: "tenant-1"},
		Action:   "update",
	})
	require.NoError(t, err)
	require.Equal(t, DecisionEffectAllow, decision.Effect, "failed replacement must retain the last valid snapshot")
}

func TestEngineRejectsTenantMismatchBeforePolicyEvaluation(t *testing.T) {
	t.Parallel()
	allow := enginePolicy("allow-member", PolicyScope{Type: PolicyScopeGlobal}, SubjectMatcher{}, EffectAllow)
	engine := newTestEngine(t, allow)
	decision, err := engine.Evaluate(t.Context(), EvaluationRequest{
		Subject:  Subject{TenantID: "tenant-1"},
		Resource: Resource{Type: "member", TenantID: "tenant-2"},
		Action:   "update",
	})
	require.NoError(t, err)
	require.Equal(t, DecisionEffectDeny, decision.Effect)
	require.Equal(t, ReasonTenantMismatch, decision.ReasonCode)
}

func TestEnginePoliciesReturnsDeepCopy(t *testing.T) {
	t.Parallel()
	authenticated := true
	policy := enginePolicy(
		"allow-member",
		PolicyScope{Type: PolicyScopeGlobal},
		SubjectMatcher{Authenticated: &authenticated, Roles: RolesMatcher{AnyOf: []string{"manager"}}},
		EffectAllow,
	)
	engine := newTestEngine(t, policy)

	policies, err := engine.Policies()
	require.NoError(t, err)
	*policies[0].Spec.Subject.Authenticated = false
	policies[0].Spec.Subject.Roles.AnyOf[0] = "changed"
	policies[0].Spec.Actions[0] = "changed"

	unchanged, err := engine.Policies()
	require.NoError(t, err)
	require.True(t, *unchanged[0].Spec.Subject.Authenticated)
	require.Equal(t, []string{"manager"}, unchanged[0].Spec.Subject.Roles.AnyOf)
	require.Equal(t, []string{"update"}, unchanged[0].Spec.Actions)
}
