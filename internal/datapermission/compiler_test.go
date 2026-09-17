package datapermission

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func memberSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := NewSchema("tenant.member", map[string]Field{
		"created_by":    {Column: "tm.created_by", Type: ValueTypeText},
		"department_id": {Column: "tm.department_id", Type: ValueTypeText},
		"status":        {Column: "tm.status", Type: ValueTypeText},
	})
	require.NoError(t, err)
	return schema
}

func TestCompilerCombinesAllowUnionAndDenyExclusion(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler(memberSchema(t))
	policies := []PolicyScope{
		{Effect: EffectAllow, Predicate: Equal(ResourceField("created_by"), SubjectField("id"))},
		{Effect: EffectAllow, Predicate: In(ResourceField("department_id"), SubjectField("department_ids"))},
		{Effect: EffectDeny, Predicate: Equal(ResourceField("status"), Literal("confidential"))},
	}
	compiled, err := compiler.Compile(policies, SubjectAttributes{
		"id": "user-1", "department_ids": []string{"department-1", "department-2"},
	})
	require.NoError(t, err)
	require.Equal(t, "(((tm.created_by = ?) OR (tm.department_id IN (?, ?))) AND (NOT (tm.status = ?)))", compiled.Clause)
	require.Equal(t, []any{"user-1", "department-1", "department-2", "confidential"}, compiled.Args)
}

func TestCompilerDefaultsToNoRowsWithoutAllow(t *testing.T) {
	t.Parallel()
	compiled, err := NewCompiler(memberSchema(t)).Compile([]PolicyScope{{Effect: EffectDeny, Predicate: Equal(ResourceField("status"), Literal("confidential"))}}, SubjectAttributes{})
	require.NoError(t, err)
	require.Equal(t, "(1 = 0)", compiled.Clause)
	require.Empty(t, compiled.Args)
}

func TestCompilerValidatesDenyPoliciesEvenWithoutAllow(t *testing.T) {
	t.Parallel()
	_, err := NewCompiler(memberSchema(t)).Compile([]PolicyScope{{
		Effect: EffectDeny, Predicate: Equal(ResourceField("unknown"), Literal("value")),
	}}, SubjectAttributes{})
	require.ErrorIs(t, err, ErrResourceFieldUnknown)
}

func TestCompilerEmptyConditionsHaveExplicitSemantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		policies []PolicyScope
		clause   string
	}{
		{name: "empty allow grants all rows", policies: []PolicyScope{{Effect: EffectAllow}}, clause: "(1 = 1)"},
		{name: "empty deny removes all allowed rows", policies: []PolicyScope{{Effect: EffectAllow}, {Effect: EffectDeny}}, clause: "((1 = 1) AND (NOT (1 = 1)))"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			compiled, err := NewCompiler(memberSchema(t)).Compile(test.policies, SubjectAttributes{})
			require.NoError(t, err)
			require.Equal(t, test.clause, compiled.Clause)
		})
	}
}

func TestCompilerDefersProposedStateDenyUntilTransition(t *testing.T) {
	compiler := NewCompiler(memberSchema(t))
	result, err := compiler.Compile([]PolicyScope{
		{Effect: EffectAllow, Predicate: Equal(ResourceField("created_by"), SubjectField("id"))},
		{Effect: EffectDeny, Predicate: Predicate{}, ProposedPredicate: Equal(ProposedField("status"), Literal("disabled")), HasProposed: true},
	}, SubjectAttributes{"id": "user-1"})
	require.NoError(t, err)
	require.Equal(t, "(tm.created_by = ?)", result.Clause)
	require.Equal(t, []any{"user-1"}, result.Args)

	evaluator := NewEvaluator(memberSchema(t))
	policies := []PolicyScope{
		{Effect: EffectAllow, Predicate: Equal(ResourceField("created_by"), SubjectField("id"))},
		{Effect: EffectDeny, ProposedPredicate: Equal(ProposedField("status"), Literal("disabled")), HasProposed: true},
	}
	allowed, err := evaluator.EvaluateTransition(policies, SubjectAttributes{"id": "user-1"}, ResourceAttributes{"created_by": "user-1"}, ResourceAttributes{"status": "active"})
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = evaluator.EvaluateTransition(policies, SubjectAttributes{"id": "user-1"}, ResourceAttributes{"created_by": "user-1"}, ResourceAttributes{"status": "disabled"})
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestCompilerFailsClosedForMissingSubjectAttribute(t *testing.T) {
	t.Parallel()
	_, err := NewCompiler(memberSchema(t)).Compile([]PolicyScope{{Effect: EffectAllow, Predicate: Equal(ResourceField("created_by"), SubjectField("id"))}}, SubjectAttributes{})
	require.ErrorIs(t, err, ErrSubjectAttributeMissing)
}

func TestCompilerRejectsUnknownResourceField(t *testing.T) {
	t.Parallel()
	_, err := NewCompiler(memberSchema(t)).Compile([]PolicyScope{{Effect: EffectAllow, Predicate: Equal(ResourceField("password_hash"), Literal("value"))}}, SubjectAttributes{})
	require.ErrorIs(t, err, ErrResourceFieldUnknown)
}

func TestNewSchemaRejectsUnsafeColumnMapping(t *testing.T) {
	t.Parallel()
	_, err := NewSchema("tenant.member", map[string]Field{"status": {Column: "tm.status) OR 1=1 --", Type: ValueTypeText}})
	require.True(t, errors.Is(err, ErrInvalidSchema))
}
