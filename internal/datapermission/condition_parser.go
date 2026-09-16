package datapermission

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
)

const MaxConditionLength = 4096

var (
	ErrInvalidCondition     = errors.New("data permission: invalid condition")
	ErrUnsupportedCondition = errors.New("data permission: unsupported condition")
)

// ConditionParser compiles a restricted CEL condition into Predicate IR. It
// never creates a CEL runtime program, so repositories cannot execute arbitrary
// functions or receive policy text.
type ConditionParser struct {
	schema      *Schema
	environment *cel.Env
}

// NewConditionParser creates the fixed CEL type-checking environment used for
// data conditions. Request and environment roots are declared only so they can
// be rejected with a stable unsupported-condition error during IR conversion.
func NewConditionParser(schema *Schema) (*ConditionParser, error) {
	if schema == nil {
		return nil, ErrInvalidSchema
	}
	environment, err := cel.NewEnv(
		cel.Variable("subject", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("resource", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("request", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("environment", cel.MapType(cel.StringType, cel.DynType)),
	)
	if err != nil {
		return nil, fmt.Errorf("create data permission CEL environment: %w", err)
	}
	return &ConditionParser{schema: schema, environment: environment}, nil
}

// Parse validates CEL syntax and converts only the documented safe subset.
func (p *ConditionParser) Parse(source string) (Predicate, error) {
	if p == nil || p.schema == nil || p.environment == nil {
		return Predicate{}, ErrInvalidSchema
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return Predicate{}, nil
	}
	if len(source) > MaxConditionLength {
		return Predicate{}, fmt.Errorf("%w: condition exceeds %d bytes", ErrInvalidCondition, MaxConditionLength)
	}
	checked, issues := p.environment.Compile(source)
	if issues.Err() != nil {
		return Predicate{}, fmt.Errorf("%w: %v", ErrInvalidCondition, issues.Err())
	}
	if checked.OutputType() != cel.BoolType {
		return Predicate{}, fmt.Errorf("%w: condition must return bool", ErrInvalidCondition)
	}
	nodes := 0
	predicate, err := p.convert(checked.NativeRep().Expr(), 1, &nodes)
	if err != nil {
		return Predicate{}, err
	}
	return predicate, nil
}

func (p *ConditionParser) convert(expression celast.Expr, depth int, nodes *int) (Predicate, error) {
	*nodes = *nodes + 1
	if depth > MaxPredicateDepth || *nodes > MaxPredicateNodes {
		return Predicate{}, ErrPredicateTooComplex
	}
	if expression == nil || expression.Kind() != celast.CallKind {
		return Predicate{}, fmt.Errorf("%w: expected boolean operator", ErrUnsupportedCondition)
	}
	call := expression.AsCall()
	if call.IsMemberFunction() {
		return Predicate{}, fmt.Errorf("%w: member functions are not allowed", ErrUnsupportedCondition)
	}
	arguments := call.Args()
	switch call.FunctionName() {
	case operators.LogicalAnd, operators.LogicalOr:
		if len(arguments) != 2 {
			return Predicate{}, ErrInvalidCondition
		}
		left, err := p.convert(arguments[0], depth+1, nodes)
		if err != nil {
			return Predicate{}, err
		}
		right, err := p.convert(arguments[1], depth+1, nodes)
		if err != nil {
			return Predicate{}, err
		}
		if call.FunctionName() == operators.LogicalAnd {
			return And(left, right), nil
		}
		return Or(left, right), nil
	case operators.LogicalNot:
		if len(arguments) != 1 {
			return Predicate{}, ErrInvalidCondition
		}
		child, err := p.convert(arguments[0], depth+1, nodes)
		if err != nil {
			return Predicate{}, err
		}
		return Not(child), nil
	case operators.Equals:
		if len(arguments) != 2 {
			return Predicate{}, ErrInvalidCondition
		}
		left, err := p.operand(arguments[0], false)
		if err != nil {
			return Predicate{}, err
		}
		right, err := p.operand(arguments[1], true)
		if err != nil {
			return Predicate{}, err
		}
		if left.kind != operandResource || (right.kind != operandSubject && right.kind != operandLiteral) {
			return Predicate{}, fmt.Errorf("%w: equality requires resource field and subject field or literal", ErrUnsupportedCondition)
		}
		if right.kind == operandSubject {
			valueType, _ := subjectFieldType(right.name)
			if valueType != subjectValueText {
				return Predicate{}, fmt.Errorf("%w: equality requires a scalar subject field", ErrUnsupportedCondition)
			}
		}
		return Equal(left, right), nil
	case operators.In, operators.OldIn:
		if len(arguments) != 2 {
			return Predicate{}, ErrInvalidCondition
		}
		left, err := p.operand(arguments[0], false)
		if err != nil {
			return Predicate{}, err
		}
		right, err := p.operand(arguments[1], false)
		if err != nil {
			return Predicate{}, err
		}
		if left.kind != operandResource || right.kind != operandSubject {
			return Predicate{}, fmt.Errorf("%w: in requires resource field and subject collection", ErrUnsupportedCondition)
		}
		valueType, _ := subjectFieldType(right.name)
		if valueType != subjectValueTextList {
			return Predicate{}, fmt.Errorf("%w: in requires a subject collection", ErrUnsupportedCondition)
		}
		return In(left, right), nil
	default:
		return Predicate{}, fmt.Errorf("%w: CEL function %q", ErrUnsupportedCondition, call.FunctionName())
	}
}

func (p *ConditionParser) operand(expression celast.Expr, allowLiteral bool) (Operand, error) {
	switch expression.Kind() {
	case celast.SelectKind:
		selection := expression.AsSelect()
		if selection.IsTestOnly() || selection.Operand().Kind() != celast.IdentKind {
			return Operand{}, fmt.Errorf("%w: nested or presence field access", ErrUnsupportedCondition)
		}
		root := selection.Operand().AsIdent()
		name := selection.FieldName()
		if !fieldNamePattern.MatchString(name) {
			return Operand{}, fmt.Errorf("%w: invalid field", ErrUnsupportedCondition)
		}
		switch root {
		case "resource":
			if _, ok := p.schema.field(name); !ok {
				return Operand{}, fmt.Errorf("%w: %q", ErrResourceFieldUnknown, name)
			}
			return ResourceField(name), nil
		case "subject":
			if _, ok := subjectFieldType(name); !ok {
				return Operand{}, fmt.Errorf("%w: %q", ErrSubjectAttributeUnknown, name)
			}
			return SubjectField(name), nil
		default:
			return Operand{}, fmt.Errorf("%w: root %q", ErrUnsupportedCondition, root)
		}
	case celast.LiteralKind:
		if !allowLiteral {
			return Operand{}, fmt.Errorf("%w: literal is not valid here", ErrUnsupportedCondition)
		}
		value := expression.AsLiteral().Value()
		if _, ok := value.(string); !ok {
			return Operand{}, fmt.Errorf("%w: only text literals are supported", ErrUnsupportedCondition)
		}
		return Literal(value), nil
	default:
		return Operand{}, fmt.Errorf("%w: operand kind %v", ErrUnsupportedCondition, expression.Kind())
	}
}
