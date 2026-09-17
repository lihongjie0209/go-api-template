package pbac

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/types"
)

const (
	MaxWhenLength = 4096
	maxWhenNodes  = 256
	maxWhenCost   = 1000
)

var (
	ErrInvalidWhen     = errors.New("pbac: invalid when condition")
	ErrUnsupportedWhen = errors.New("pbac: unsupported when condition")
)

var operationAttributeSchema = map[string]map[string]struct{}{
	"subject":        fields("id", "type", "authenticated", "tenant_id", "membership_id", "roles"),
	"tenant":         fields("id"),
	"request":        fields("transport", "operation"),
	"environment":    fields("profile", "timezone", "local_hour", "weekday", "business_day"),
	"authentication": fields("scheme"),
}

type whenValueType uint8

const (
	whenTypeBool whenValueType = iota + 1
	whenTypeString
	whenTypeInt
	whenTypeStringList
	whenTypeIntList
)

var operationAttributeTypes = map[string]map[string]whenValueType{
	"subject":        {"id": whenTypeString, "type": whenTypeString, "authenticated": whenTypeBool, "tenant_id": whenTypeString, "membership_id": whenTypeString, "roles": whenTypeStringList},
	"tenant":         {"id": whenTypeString},
	"request":        {"transport": whenTypeString, "operation": whenTypeString},
	"environment":    {"profile": whenTypeString, "timezone": whenTypeString, "local_hour": whenTypeInt, "weekday": whenTypeInt, "business_day": whenTypeBool},
	"authentication": {"scheme": whenTypeString},
}

var allowedWhenOperators = map[string]struct{}{
	operators.LogicalAnd: {}, operators.LogicalOr: {}, operators.LogicalNot: {},
	operators.Equals: {}, operators.NotEquals: {},
	operators.Less: {}, operators.LessEquals: {}, operators.Greater: {}, operators.GreaterEquals: {},
	operators.In: {}, operators.OldIn: {},
}

type compiledWhen struct{ program cel.Program }

func compileWhen(source string) (*compiledWhen, error) {
	if len(source) > MaxWhenLength {
		return nil, fmt.Errorf("%w: expression exceeds %d bytes", ErrInvalidWhen, MaxWhenLength)
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, nil
	}
	environment, err := cel.NewEnv(
		cel.ClearMacros(),
		cel.Variable("subject", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("tenant", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("request", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("environment", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("authentication", cel.MapType(cel.StringType, cel.DynType)),
	)
	if err != nil {
		return nil, fmt.Errorf("create operation condition environment: %w", err)
	}
	checked, issues := environment.Compile(source)
	if issues.Err() != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidWhen, issues.Err())
	}
	if checked.OutputType() != cel.BoolType && (checked.OutputType() != cel.DynType || !booleanWhenExpression(checked.NativeRep().Expr())) {
		return nil, fmt.Errorf("%w: expression must return bool", ErrInvalidWhen)
	}
	nodes := 0
	if err := validateWhenExpression(checked.NativeRep().Expr(), &nodes); err != nil {
		return nil, err
	}
	resultType, err := inferWhenType(checked.NativeRep().Expr())
	if err != nil {
		return nil, err
	}
	if resultType != whenTypeBool {
		return nil, fmt.Errorf("%w: expression must return bool", ErrInvalidWhen)
	}
	program, err := environment.Program(checked, cel.CostLimit(maxWhenCost))
	if err != nil {
		return nil, fmt.Errorf("%w: compile program: %v", ErrInvalidWhen, err)
	}
	return &compiledWhen{program: program}, nil
}

func inferWhenType(expression celast.Expr) (whenValueType, error) {
	switch expression.Kind() {
	case celast.SelectKind:
		selection := expression.AsSelect()
		return operationAttributeTypes[selection.Operand().AsIdent()][selection.FieldName()], nil
	case celast.LiteralKind:
		switch expression.AsLiteral().(type) {
		case types.Bool:
			return whenTypeBool, nil
		case types.String:
			return whenTypeString, nil
		case types.Int:
			return whenTypeInt, nil
		default:
			return 0, fmt.Errorf("%w: unsupported literal type", ErrUnsupportedWhen)
		}
	case celast.ListKind:
		elements := expression.AsList().Elements()
		if len(elements) == 0 {
			return 0, fmt.Errorf("%w: lists must not be empty", ErrUnsupportedWhen)
		}
		first, err := inferWhenType(elements[0])
		if err != nil {
			return 0, err
		}
		for _, element := range elements[1:] {
			current, elementErr := inferWhenType(element)
			if elementErr != nil {
				return 0, elementErr
			}
			if current != first {
				return 0, fmt.Errorf("%w: list values must have one type", ErrUnsupportedWhen)
			}
		}
		switch first {
		case whenTypeString:
			return whenTypeStringList, nil
		case whenTypeInt:
			return whenTypeIntList, nil
		default:
			return 0, fmt.Errorf("%w: only string and integer lists are allowed", ErrUnsupportedWhen)
		}
	case celast.CallKind:
		call := expression.AsCall()
		arguments := call.Args()
		switch call.FunctionName() {
		case operators.LogicalNot:
			if len(arguments) != 1 {
				return 0, ErrInvalidWhen
			}
			valueType, err := inferWhenType(arguments[0])
			if err != nil || valueType != whenTypeBool {
				return 0, fmt.Errorf("%w: logical not requires boolean", ErrUnsupportedWhen)
			}
		case operators.LogicalAnd, operators.LogicalOr:
			if len(arguments) != 2 {
				return 0, ErrInvalidWhen
			}
			for _, argument := range arguments {
				valueType, err := inferWhenType(argument)
				if err != nil || valueType != whenTypeBool {
					return 0, fmt.Errorf("%w: logical operators require booleans", ErrUnsupportedWhen)
				}
			}
		case operators.Equals, operators.NotEquals:
			if len(arguments) != 2 {
				return 0, ErrInvalidWhen
			}
			left, err := inferWhenType(arguments[0])
			if err != nil {
				return 0, err
			}
			right, err := inferWhenType(arguments[1])
			if err != nil || left != right || left == whenTypeStringList || left == whenTypeIntList {
				return 0, fmt.Errorf("%w: equality operands must have the same scalar type", ErrUnsupportedWhen)
			}
		case operators.Less, operators.LessEquals, operators.Greater, operators.GreaterEquals:
			if len(arguments) != 2 {
				return 0, ErrInvalidWhen
			}
			left, leftErr := inferWhenType(arguments[0])
			right, rightErr := inferWhenType(arguments[1])
			if leftErr != nil || rightErr != nil || left != whenTypeInt || right != whenTypeInt {
				return 0, fmt.Errorf("%w: ordered comparisons require integers", ErrUnsupportedWhen)
			}
		case operators.In, operators.OldIn:
			if len(arguments) != 2 {
				return 0, ErrInvalidWhen
			}
			left, leftErr := inferWhenType(arguments[0])
			right, rightErr := inferWhenType(arguments[1])
			if leftErr != nil || rightErr != nil || left == whenTypeString && right != whenTypeStringList || left == whenTypeInt && right != whenTypeIntList || left != whenTypeString && left != whenTypeInt {
				return 0, fmt.Errorf("%w: in operands have incompatible types", ErrUnsupportedWhen)
			}
		default:
			return 0, fmt.Errorf("%w: operator %q", ErrUnsupportedWhen, call.FunctionName())
		}
		return whenTypeBool, nil
	default:
		return 0, fmt.Errorf("%w: expression kind %v", ErrUnsupportedWhen, expression.Kind())
	}
}

func booleanWhenExpression(expression celast.Expr) bool {
	if expression == nil {
		return false
	}
	if expression.Kind() == celast.CallKind {
		_, ok := allowedWhenOperators[expression.AsCall().FunctionName()]
		return ok
	}
	if expression.Kind() != celast.SelectKind {
		return false
	}
	selection := expression.AsSelect()
	if selection.Operand().Kind() != celast.IdentKind {
		return false
	}
	root, field := selection.Operand().AsIdent(), selection.FieldName()
	return root == "environment" && field == "business_day" || root == "subject" && field == "authenticated"
}

func validateWhenExpression(expression celast.Expr, nodes *int) error {
	if expression == nil {
		return ErrInvalidWhen
	}
	*nodes = *nodes + 1
	if *nodes > maxWhenNodes {
		return fmt.Errorf("%w: expression exceeds %d nodes", ErrInvalidWhen, maxWhenNodes)
	}
	switch expression.Kind() {
	case celast.CallKind:
		call := expression.AsCall()
		if call.IsMemberFunction() {
			return fmt.Errorf("%w: member functions are not allowed", ErrUnsupportedWhen)
		}
		if _, ok := allowedWhenOperators[call.FunctionName()]; !ok {
			return fmt.Errorf("%w: operator %q", ErrUnsupportedWhen, call.FunctionName())
		}
		for _, argument := range call.Args() {
			if err := validateWhenExpression(argument, nodes); err != nil {
				return err
			}
		}
		return nil
	case celast.SelectKind:
		selection := expression.AsSelect()
		if selection.IsTestOnly() || selection.Operand().Kind() != celast.IdentKind {
			return fmt.Errorf("%w: nested or presence field access", ErrUnsupportedWhen)
		}
		root := selection.Operand().AsIdent()
		allowed, ok := operationAttributeSchema[root]
		if !ok {
			return fmt.Errorf("%w: root %q", ErrUnsupportedWhen, root)
		}
		if _, ok := allowed[selection.FieldName()]; !ok {
			return fmt.Errorf("%w: field %s.%s", ErrUnsupportedWhen, root, selection.FieldName())
		}
		return nil
	case celast.ListKind:
		if expression.AsList().Size() > MaxMatcherValues {
			return fmt.Errorf("%w: list exceeds %d values", ErrInvalidWhen, MaxMatcherValues)
		}
		for _, element := range expression.AsList().Elements() {
			*nodes = *nodes + 1
			if *nodes > maxWhenNodes {
				return fmt.Errorf("%w: expression exceeds %d nodes", ErrInvalidWhen, maxWhenNodes)
			}
			if element.Kind() != celast.LiteralKind {
				return fmt.Errorf("%w: list values must be literals", ErrUnsupportedWhen)
			}
		}
		return nil
	case celast.LiteralKind:
		return nil
	default:
		return fmt.Errorf("%w: expression kind %v", ErrUnsupportedWhen, expression.Kind())
	}
}

func (condition *compiledWhen) evaluate(ctx context.Context, request EvaluationRequest) (bool, error) {
	if condition == nil {
		return true, nil
	}
	value, _, err := condition.program.ContextEval(ctx, map[string]any{
		"subject": map[string]any{
			"id": request.Subject.ID, "type": request.Subject.Type, "authenticated": request.Subject.Authenticated,
			"tenant_id": request.Subject.TenantID, "membership_id": request.Subject.MembershipID, "roles": request.Subject.Roles,
		},
		"tenant":  map[string]any{"id": request.Resource.TenantID},
		"request": map[string]any{"transport": request.Context.Transport, "operation": request.Context.Operation},
		"environment": map[string]any{
			"profile": request.Context.Profile, "timezone": request.Context.Timezone,
			"local_hour": request.Context.LocalHour, "weekday": request.Context.Weekday,
			"business_day": request.Context.BusinessDay,
		},
		"authentication": map[string]any{"scheme": request.Context.AuthenticationScheme},
	})
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrInvalidWhen, err)
	}
	result, ok := value.(types.Bool)
	if !ok {
		return false, fmt.Errorf("%w: non-boolean result", ErrInvalidWhen)
	}
	return bool(result), nil
}

func fields(names ...string) map[string]struct{} {
	values := make(map[string]struct{}, len(names))
	for _, name := range names {
		values[name] = struct{}{}
	}
	return values
}
