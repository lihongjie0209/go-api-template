package datapermission

import (
	"fmt"
	"strings"
)

// SQLPredicate is a parameterized, database-neutral WHERE fragment.
type SQLPredicate struct {
	Clause string
	Args   []any
}

// Compiler renders immutable Predicate IR through one Resource Schema.
type Compiler struct {
	schema *Schema
}

// NewCompiler creates a compiler for one Resource Schema.
func NewCompiler(schema *Schema) *Compiler { return &Compiler{schema: schema} }

// Compile unions Allow scopes and subtracts the union of Deny scopes.
func (c *Compiler) Compile(policies []PolicyScope, subject SubjectAttributes) (SQLPredicate, error) {
	if c == nil || c.schema == nil {
		return SQLPredicate{}, ErrInvalidSchema
	}
	allows := make([]Predicate, 0, len(policies))
	denies := make([]Predicate, 0, len(policies))
	for _, policy := range policies {
		switch policy.Effect {
		case EffectAllow:
			allows = append(allows, policy.Predicate)
		case EffectDeny:
			denies = append(denies, policy.Predicate)
		default:
			return SQLPredicate{}, fmt.Errorf("%w: unknown effect %q", ErrInvalidPredicate, policy.Effect)
		}
	}
	var deny SQLPredicate
	if len(denies) > 0 {
		var err error
		deny, err = c.compileUnion(denies, subject)
		if err != nil {
			return SQLPredicate{}, err
		}
	}
	if len(allows) == 0 {
		return SQLPredicate{Clause: "(1 = 0)", Args: []any{}}, nil
	}
	allow, err := c.compileUnion(allows, subject)
	if err != nil {
		return SQLPredicate{}, err
	}
	if len(denies) == 0 {
		return allow, nil
	}
	return SQLPredicate{
		Clause: "(" + allow.Clause + " AND (NOT " + deny.Clause + "))",
		Args:   append(append([]any{}, allow.Args...), deny.Args...),
	}, nil
}

func (c *Compiler) compileUnion(predicates []Predicate, subject SubjectAttributes) (SQLPredicate, error) {
	compiled := make([]SQLPredicate, 0, len(predicates))
	for _, predicate := range predicates {
		result, err := c.compilePredicate(predicate, subject, 1, new(int))
		if err != nil {
			return SQLPredicate{}, err
		}
		compiled = append(compiled, result)
	}
	if len(compiled) == 1 {
		return compiled[0], nil
	}
	clauses := make([]string, 0, len(compiled))
	args := make([]any, 0)
	for _, item := range compiled {
		clauses = append(clauses, item.Clause)
		args = append(args, item.Args...)
	}
	return SQLPredicate{Clause: "(" + strings.Join(clauses, " OR ") + ")", Args: args}, nil
}

func (c *Compiler) compilePredicate(predicate Predicate, subject SubjectAttributes, depth int, nodes *int) (SQLPredicate, error) {
	*nodes = *nodes + 1
	if depth > MaxPredicateDepth || *nodes > MaxPredicateNodes {
		return SQLPredicate{}, ErrPredicateTooComplex
	}
	switch predicate.kind {
	case predicateEmpty:
		return SQLPredicate{Clause: "(1 = 1)", Args: []any{}}, nil
	case predicateEqual:
		return c.compileEqual(predicate.left, predicate.right, subject)
	case predicateIn:
		return c.compileIn(predicate.left, predicate.right, subject)
	case predicateAnd, predicateOr:
		if len(predicate.children) < 2 {
			return SQLPredicate{}, fmt.Errorf("%w: boolean node requires at least two children", ErrInvalidPredicate)
		}
		parts := make([]SQLPredicate, 0, len(predicate.children))
		for _, child := range predicate.children {
			part, err := c.compilePredicate(child, subject, depth+1, nodes)
			if err != nil {
				return SQLPredicate{}, err
			}
			parts = append(parts, part)
		}
		operator := " AND "
		if predicate.kind == predicateOr {
			operator = " OR "
		}
		clauses := make([]string, 0, len(parts))
		args := make([]any, 0)
		for _, part := range parts {
			clauses = append(clauses, part.Clause)
			args = append(args, part.Args...)
		}
		return SQLPredicate{Clause: "(" + strings.Join(clauses, operator) + ")", Args: args}, nil
	case predicateNot:
		if len(predicate.children) != 1 {
			return SQLPredicate{}, fmt.Errorf("%w: not requires one child", ErrInvalidPredicate)
		}
		child, err := c.compilePredicate(predicate.children[0], subject, depth+1, nodes)
		if err != nil {
			return SQLPredicate{}, err
		}
		return SQLPredicate{Clause: "(NOT " + child.Clause + ")", Args: child.Args}, nil
	default:
		return SQLPredicate{}, ErrInvalidPredicate
	}
}

func (c *Compiler) compileEqual(left, right Operand, subject SubjectAttributes) (SQLPredicate, error) {
	if right.kind == operandSubject {
		valueType, ok := subjectFieldType(right.name)
		if !ok {
			return SQLPredicate{}, fmt.Errorf("%w: %q", ErrSubjectAttributeUnknown, right.name)
		}
		if valueType != subjectValueText {
			return SQLPredicate{}, ErrAttributeTypeMismatch
		}
	}
	field, value, err := c.resolveComparison(left, right, subject)
	if err != nil {
		return SQLPredicate{}, err
	}
	if _, ok := value.(string); !ok {
		return SQLPredicate{}, ErrAttributeTypeMismatch
	}
	return SQLPredicate{Clause: "(" + field.Column + " = ?)", Args: []any{value}}, nil
}

func (c *Compiler) compileIn(left, right Operand, subject SubjectAttributes) (SQLPredicate, error) {
	if left.kind != operandResource || right.kind != operandSubject {
		return SQLPredicate{}, fmt.Errorf("%w: in requires resource field and subject collection", ErrInvalidPredicate)
	}
	valueType, ok := subjectFieldType(right.name)
	if !ok {
		return SQLPredicate{}, fmt.Errorf("%w: %q", ErrSubjectAttributeUnknown, right.name)
	}
	if valueType != subjectValueTextList {
		return SQLPredicate{}, ErrAttributeTypeMismatch
	}
	field, ok := c.schema.field(left.name)
	if !ok {
		return SQLPredicate{}, fmt.Errorf("%w: %q", ErrResourceFieldUnknown, left.name)
	}
	value, ok := subject[right.name]
	if !ok {
		return SQLPredicate{}, fmt.Errorf("%w: %q", ErrSubjectAttributeMissing, right.name)
	}
	values, ok := value.([]string)
	if !ok {
		return SQLPredicate{}, fmt.Errorf("%w: subject.%s", ErrAttributeTypeMismatch, right.name)
	}
	if len(values) == 0 {
		return SQLPredicate{Clause: "(1 = 0)", Args: []any{}}, nil
	}
	args := make([]any, len(values))
	placeholders := make([]string, len(values))
	for index, item := range values {
		args[index] = item
		placeholders[index] = "?"
	}
	return SQLPredicate{Clause: "(" + field.Column + " IN (" + strings.Join(placeholders, ", ") + "))", Args: args}, nil
}

func (c *Compiler) resolveComparison(left, right Operand, subject SubjectAttributes) (Field, any, error) {
	if left.kind != operandResource {
		return Field{}, nil, fmt.Errorf("%w: comparison left operand must be a resource field", ErrInvalidPredicate)
	}
	field, ok := c.schema.field(left.name)
	if !ok {
		return Field{}, nil, fmt.Errorf("%w: %q", ErrResourceFieldUnknown, left.name)
	}
	switch right.kind {
	case operandSubject:
		value, exists := subject[right.name]
		if !exists {
			return Field{}, nil, fmt.Errorf("%w: %q", ErrSubjectAttributeMissing, right.name)
		}
		return field, value, nil
	case operandLiteral:
		return field, right.literal, nil
	default:
		return Field{}, nil, fmt.Errorf("%w: comparison right operand must be subject or literal", ErrInvalidPredicate)
	}
}
