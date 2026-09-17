package pbac

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestActionPolicyWhenRestrictsTrustedOperationContext(t *testing.T) {
	t.Parallel()
	policy := enginePolicy("working-hours", PolicyScope{Type: PolicyScopeGlobal}, SubjectMatcher{}, EffectAllow)
	policy.Spec.When = `environment.business_day && environment.local_hour >= 9 && environment.local_hour < 18 && authentication.scheme == "bearer"`
	engine := newTestEngine(t, policy)
	request := EvaluationRequest{
		Subject: Subject{TenantID: "tenant-1"}, Resource: Resource{Type: "member", TenantID: "tenant-1"}, Action: "update",
		Context: OperationContext{LocalHour: 9, Weekday: 1, BusinessDay: true, AuthenticationScheme: "bearer"},
	}

	decision, err := engine.Evaluate(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, DecisionEffectAllow, decision.Effect)

	request.Context.LocalHour = 18
	decision, err = engine.Evaluate(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, DecisionEffectDeny, decision.Effect)
	require.Equal(t, ReasonNoMatchingPolicy, decision.ReasonCode)
}

func TestActionPolicyWhenFalseDoesNotProduceExplicitDeny(t *testing.T) {
	t.Parallel()
	allow := enginePolicy("always-allow", PolicyScope{Type: PolicyScopeGlobal}, SubjectMatcher{}, EffectAllow)
	deny := enginePolicy("deny-outside-production", PolicyScope{Type: PolicyScopeGlobal}, SubjectMatcher{}, EffectDeny)
	deny.Spec.When = `environment.profile == "production"`
	engine := newTestEngine(t, allow, deny)

	decision, err := engine.Evaluate(t.Context(), EvaluationRequest{
		Subject: Subject{TenantID: "tenant-1"}, Resource: Resource{Type: "member", TenantID: "tenant-1"}, Action: "update",
		Context: OperationContext{Profile: "development"},
	})
	require.NoError(t, err)
	require.Equal(t, DecisionEffectAllow, decision.Effect)
	require.Equal(t, []string{"always-allow"}, decision.MatchedPolicies)
}

func TestActionPolicyWhenSupportsBoundedLiteralMembership(t *testing.T) {
	t.Parallel()
	condition, err := compileWhen(`environment.weekday in [1, 2, 3, 4, 5] && "manager" in subject.roles`)
	require.NoError(t, err)
	matched, err := condition.evaluate(t.Context(), EvaluationRequest{
		Subject: Subject{Roles: []string{"manager"}}, Context: OperationContext{Weekday: 5},
	})
	require.NoError(t, err)
	require.True(t, matched)

	_, err = compileWhen(`environment.profile in [subject.type]`)
	require.ErrorIs(t, err, ErrUnsupportedWhen)
}

func TestCompileWhenRejectsUntrustedOrUnboundedExpressions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		target error
	}{
		{name: "resource field", source: `resource.status == "active"`, target: ErrInvalidWhen},
		{name: "proposed field", source: `proposed.status == "active"`, target: ErrInvalidWhen},
		{name: "unknown approved-root field", source: `environment.secret == "x"`, target: ErrUnsupportedWhen},
		{name: "member function", source: `request.operation.startsWith("POST")`, target: ErrUnsupportedWhen},
		{name: "dynamic index", source: `environment["profile"] == "production"`, target: ErrUnsupportedWhen},
		{name: "comparison type mismatch", source: `environment.local_hour == "9"`, target: ErrUnsupportedWhen},
		{name: "ordered string comparison", source: `environment.profile > "development"`, target: ErrUnsupportedWhen},
		{name: "mixed list types", source: `environment.weekday in [1, "2"]`, target: ErrUnsupportedWhen},
		{name: "not boolean", source: `environment.local_hour`, target: ErrInvalidWhen},
		{name: "too long", source: strings.Repeat(" ", MaxWhenLength) + "true", target: ErrInvalidWhen},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := compileWhen(test.source)
			require.ErrorIs(t, err, test.target)
		})
	}
}

func TestActionPolicyWhenEvaluationCancellationFailsClosed(t *testing.T) {
	t.Parallel()
	policy := enginePolicy("conditional", PolicyScope{Type: PolicyScopeGlobal}, SubjectMatcher{}, EffectAllow)
	policy.Spec.When = `environment.business_day`
	engine := newTestEngine(t, policy)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	decision, err := engine.Evaluate(ctx, EvaluationRequest{
		Subject: Subject{TenantID: "tenant-1"}, Resource: Resource{Type: "member", TenantID: "tenant-1"}, Action: "update",
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, DecisionEffectIndeterminate, decision.Effect)
}
