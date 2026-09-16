package datapermission

import "fmt"

// ResourceAttributes contains trusted values from a row or proposed object.
type ResourceAttributes map[string]any

// Evaluator applies the same Predicate IR to one in-memory Resource instance.
type Evaluator struct {
	schema *Schema
}

// NewEvaluator creates an evaluator for one Resource Schema.
func NewEvaluator(schema *Schema) *Evaluator { return &Evaluator{schema: schema} }

// Evaluate returns true only when an Allow matches and no Deny matches.
func (e *Evaluator) Evaluate(policies []PolicyScope, subject SubjectAttributes, resource ResourceAttributes) (bool, error) {
	if e == nil || e.schema == nil {
		return false, ErrInvalidSchema
	}
	matchedAllow := false
	matchedDeny := false
	for _, policy := range policies {
		if policy.Effect != EffectAllow && policy.Effect != EffectDeny {
			return false, fmt.Errorf("%w: unknown effect %q", ErrInvalidPredicate, policy.Effect)
		}
		matched, err := e.evaluatePredicate(policy.Predicate, subject, resource, 1, new(int))
		if err != nil {
			return false, err
		}
		if !matched {
			continue
		}
		if policy.Effect == EffectDeny {
			matchedDeny = true
			continue
		}
		matchedAllow = true
	}
	return matchedAllow && !matchedDeny, nil
}

func (e *Evaluator) evaluatePredicate(predicate Predicate, subject SubjectAttributes, resource ResourceAttributes, depth int, nodes *int) (bool, error) {
	*nodes = *nodes + 1
	if depth > MaxPredicateDepth || *nodes > MaxPredicateNodes {
		return false, ErrPredicateTooComplex
	}
	switch predicate.kind {
	case predicateEmpty:
		return true, nil
	case predicateEqual:
		if predicate.right.kind == operandSubject {
			valueType, ok := subjectFieldType(predicate.right.name)
			if !ok {
				return false, fmt.Errorf("%w: %q", ErrSubjectAttributeUnknown, predicate.right.name)
			}
			if valueType != subjectValueText {
				return false, ErrAttributeTypeMismatch
			}
		}
		left, right, err := e.resolveValues(predicate.left, predicate.right, subject, resource)
		if err != nil {
			return false, err
		}
		leftText, leftOK := left.(string)
		rightText, rightOK := right.(string)
		if !leftOK || !rightOK {
			return false, ErrAttributeTypeMismatch
		}
		return leftText == rightText, nil
	case predicateIn:
		if predicate.left.kind != operandResource || predicate.right.kind != operandSubject {
			return false, fmt.Errorf("%w: in requires resource field and subject collection", ErrInvalidPredicate)
		}
		valueType, ok := subjectFieldType(predicate.right.name)
		if !ok {
			return false, fmt.Errorf("%w: %q", ErrSubjectAttributeUnknown, predicate.right.name)
		}
		if valueType != subjectValueTextList {
			return false, ErrAttributeTypeMismatch
		}
		value, err := e.resourceValue(predicate.left.name, resource)
		if err != nil {
			return false, err
		}
		item, ok := value.(string)
		if !ok {
			return false, ErrAttributeTypeMismatch
		}
		collection, exists := subject[predicate.right.name]
		if !exists {
			return false, fmt.Errorf("%w: %q", ErrSubjectAttributeMissing, predicate.right.name)
		}
		values, ok := collection.([]string)
		if !ok {
			return false, fmt.Errorf("%w: subject.%s", ErrAttributeTypeMismatch, predicate.right.name)
		}
		for _, candidate := range values {
			if item == candidate {
				return true, nil
			}
		}
		return false, nil
	case predicateAnd:
		if len(predicate.children) < 2 {
			return false, fmt.Errorf("%w: and requires at least two children", ErrInvalidPredicate)
		}
		matchedAll := true
		for _, child := range predicate.children {
			matched, err := e.evaluatePredicate(child, subject, resource, depth+1, nodes)
			if err != nil {
				return false, err
			}
			if !matched {
				matchedAll = false
			}
		}
		return matchedAll, nil
	case predicateOr:
		if len(predicate.children) < 2 {
			return false, fmt.Errorf("%w: or requires at least two children", ErrInvalidPredicate)
		}
		matchedAny := false
		for _, child := range predicate.children {
			matched, err := e.evaluatePredicate(child, subject, resource, depth+1, nodes)
			if err != nil {
				return false, err
			}
			if matched {
				matchedAny = true
			}
		}
		return matchedAny, nil
	case predicateNot:
		if len(predicate.children) != 1 {
			return false, fmt.Errorf("%w: not requires one child", ErrInvalidPredicate)
		}
		matched, err := e.evaluatePredicate(predicate.children[0], subject, resource, depth+1, nodes)
		return !matched, err
	default:
		return false, ErrInvalidPredicate
	}
}

func (e *Evaluator) resolveValues(left, right Operand, subject SubjectAttributes, resource ResourceAttributes) (any, any, error) {
	if left.kind != operandResource {
		return nil, nil, fmt.Errorf("%w: comparison left operand must be a resource field", ErrInvalidPredicate)
	}
	leftValue, err := e.resourceValue(left.name, resource)
	if err != nil {
		return nil, nil, err
	}
	switch right.kind {
	case operandSubject:
		rightValue, ok := subject[right.name]
		if !ok {
			return nil, nil, fmt.Errorf("%w: %q", ErrSubjectAttributeMissing, right.name)
		}
		return leftValue, rightValue, nil
	case operandLiteral:
		return leftValue, right.literal, nil
	default:
		return nil, nil, fmt.Errorf("%w: comparison right operand must be subject or literal", ErrInvalidPredicate)
	}
}

func (e *Evaluator) resourceValue(name string, resource ResourceAttributes) (any, error) {
	if _, ok := e.schema.field(name); !ok {
		return nil, fmt.Errorf("%w: %q", ErrResourceFieldUnknown, name)
	}
	value, ok := resource[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrResourceAttributeMissing, name)
	}
	return value, nil
}
