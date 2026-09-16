package datapermission

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvaluatorUsesSameAllowUnionAndDenyExclusion(t *testing.T) {
	t.Parallel()
	evaluator := NewEvaluator(memberSchema(t))
	policies := []PolicyScope{
		{Effect: EffectAllow, Predicate: Equal(ResourceField("created_by"), SubjectField("id"))},
		{Effect: EffectAllow, Predicate: In(ResourceField("department_id"), SubjectField("department_ids"))},
		{Effect: EffectDeny, Predicate: Equal(ResourceField("status"), Literal("confidential"))},
	}
	subject := SubjectAttributes{"id": "user-1", "department_ids": []string{"department-1"}}
	tests := []struct {
		name     string
		resource ResourceAttributes
		allowed  bool
	}{
		{name: "owner is allowed", resource: ResourceAttributes{"created_by": "user-1", "department_id": "other", "status": "active"}, allowed: true},
		{name: "managed department is allowed", resource: ResourceAttributes{"created_by": "other", "department_id": "department-1", "status": "active"}, allowed: true},
		{name: "deny excludes otherwise allowed row", resource: ResourceAttributes{"created_by": "user-1", "department_id": "department-1", "status": "confidential"}},
		{name: "outside allow union is denied", resource: ResourceAttributes{"created_by": "other", "department_id": "other", "status": "active"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			allowed, err := evaluator.Evaluate(policies, subject, test.resource)
			require.NoError(t, err)
			require.Equal(t, test.allowed, allowed)
		})
	}
}

func TestEvaluatorFailsClosedForMissingResourceAttribute(t *testing.T) {
	t.Parallel()
	allowed, err := NewEvaluator(memberSchema(t)).Evaluate(
		[]PolicyScope{{Effect: EffectAllow, Predicate: Equal(ResourceField("created_by"), SubjectField("id"))}},
		SubjectAttributes{"id": "user-1"},
		ResourceAttributes{},
	)
	require.False(t, allowed)
	require.ErrorIs(t, err, ErrResourceAttributeMissing)
}

func TestEvaluatorDefaultsToDenyWithoutAllow(t *testing.T) {
	t.Parallel()
	allowed, err := NewEvaluator(memberSchema(t)).Evaluate(nil, SubjectAttributes{}, ResourceAttributes{})
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestPredicateComplexityIsBounded(t *testing.T) {
	t.Parallel()
	predicate := Equal(ResourceField("status"), Literal("active"))
	for range MaxPredicateDepth {
		predicate = Not(predicate)
	}
	_, err := NewCompiler(memberSchema(t)).Compile([]PolicyScope{{Effect: EffectAllow, Predicate: predicate}}, SubjectAttributes{})
	require.ErrorIs(t, err, ErrPredicateTooComplex)
}

func TestEvaluatorDoesNotHideMissingAttributesBehindBooleanShortCircuit(t *testing.T) {
	t.Parallel()
	predicate := Or(
		Equal(ResourceField("created_by"), Literal("user-1")),
		Equal(ResourceField("department_id"), SubjectField("tenant_id")),
	)
	allowed, err := NewEvaluator(memberSchema(t)).Evaluate(
		[]PolicyScope{{Effect: EffectAllow, Predicate: predicate}},
		SubjectAttributes{},
		ResourceAttributes{"created_by": "user-1", "department_id": "department-1"},
	)
	require.False(t, allowed)
	require.ErrorIs(t, err, ErrSubjectAttributeMissing)
}

func TestEvaluatorChecksEveryMatchedPolicyBeforeReturningDeny(t *testing.T) {
	t.Parallel()
	allowed, err := NewEvaluator(memberSchema(t)).Evaluate(
		[]PolicyScope{
			{Effect: EffectDeny},
			{Effect: EffectAllow, Predicate: Equal(ResourceField("created_by"), SubjectField("membership_id"))},
		},
		SubjectAttributes{},
		ResourceAttributes{"created_by": "user-1"},
	)
	require.False(t, allowed)
	require.ErrorIs(t, err, ErrSubjectAttributeMissing)
}
