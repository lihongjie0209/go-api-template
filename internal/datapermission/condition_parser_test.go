package datapermission

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConditionParserProducesSharedPredicateIR(t *testing.T) {
	t.Parallel()
	parser, err := NewConditionParser(memberSchema(t))
	require.NoError(t, err)
	predicate, err := parser.Parse(`
        resource.created_by == subject.id
        || resource.department_id in subject.department_ids
    `)
	require.NoError(t, err)

	compiled, err := NewCompiler(memberSchema(t)).Compile(
		[]PolicyScope{{Effect: EffectAllow, Predicate: predicate}},
		SubjectAttributes{"id": "user-1", "department_ids": []string{"department-1"}},
	)
	require.NoError(t, err)
	require.Equal(t, "((tm.created_by = ?) OR (tm.department_id IN (?)))", compiled.Clause)
	require.Equal(t, []any{"user-1", "department-1"}, compiled.Args)

	allowed, err := NewEvaluator(memberSchema(t)).Evaluate(
		[]PolicyScope{{Effect: EffectAllow, Predicate: predicate}},
		SubjectAttributes{"id": "user-1", "department_ids": []string{"department-1"}},
		ResourceAttributes{"created_by": "other", "department_id": "department-1"},
	)
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestConditionParserSupportsLiteralAndNegation(t *testing.T) {
	t.Parallel()
	parser, err := NewConditionParser(memberSchema(t))
	require.NoError(t, err)
	predicate, err := parser.Parse(`!(resource.status == "confidential")`)
	require.NoError(t, err)
	compiled, err := NewCompiler(memberSchema(t)).Compile([]PolicyScope{{Effect: EffectAllow, Predicate: predicate}}, SubjectAttributes{})
	require.NoError(t, err)
	require.Equal(t, "(NOT (tm.status = ?))", compiled.Clause)
	require.Equal(t, []any{"confidential"}, compiled.Args)
}

func TestConditionParserTreatsEmptySourceAsUnrestrictedScope(t *testing.T) {
	t.Parallel()
	parser, err := NewConditionParser(memberSchema(t))
	require.NoError(t, err)
	predicate, err := parser.Parse("  ")
	require.NoError(t, err)
	compiled, err := NewCompiler(memberSchema(t)).Compile([]PolicyScope{{Effect: EffectAllow, Predicate: predicate}}, SubjectAttributes{})
	require.NoError(t, err)
	require.Equal(t, "(1 = 1)", compiled.Clause)
}

func TestConditionParserRejectsUnsupportedOrUnsafeExpressions(t *testing.T) {
	t.Parallel()
	parser, err := NewConditionParser(memberSchema(t))
	require.NoError(t, err)
	tests := []struct {
		name   string
		source string
		target error
	}{
		{name: "request context", source: `request.client_ip == "127.0.0.1"`, target: ErrUnsupportedCondition},
		{name: "function", source: `resource.status.startsWith("active")`, target: ErrUnsupportedCondition},
		{name: "unknown resource field", source: `resource.password_hash == "secret"`, target: ErrResourceFieldUnknown},
		{name: "unknown subject field", source: `resource.created_by == subject.client_supplied_id`, target: ErrSubjectAttributeUnknown},
		{name: "collection used as scalar", source: `resource.created_by == subject.department_ids`, target: ErrUnsupportedCondition},
		{name: "scalar used as collection", source: `resource.created_by in subject.id`, target: ErrUnsupportedCondition},
		{name: "resource to resource", source: `resource.status == resource.created_by`, target: ErrUnsupportedCondition},
		{name: "malformed", source: `resource.status ==`, target: ErrInvalidCondition},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, parseErr := parser.Parse(test.source)
			require.ErrorIs(t, parseErr, test.target)
		})
	}
}
