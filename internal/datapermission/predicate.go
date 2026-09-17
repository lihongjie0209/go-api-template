package datapermission

import "errors"

const (
	// MaxPredicateNodes bounds one data condition's total IR nodes.
	MaxPredicateNodes = 256
	// MaxPredicateDepth bounds nested boolean expressions.
	MaxPredicateDepth = 32
)

var (
	ErrInvalidPredicate         = errors.New("data permission: invalid predicate")
	ErrPredicateTooComplex      = errors.New("data permission: predicate too complex")
	ErrResourceFieldUnknown     = errors.New("data permission: resource field unknown")
	ErrResourceAttributeMissing = errors.New("data permission: resource attribute missing")
	ErrSubjectAttributeMissing  = errors.New("data permission: subject attribute missing")
	ErrSubjectAttributeUnknown  = errors.New("data permission: subject attribute unknown")
	ErrAttributeTypeMismatch    = errors.New("data permission: attribute type mismatch")
	ErrInvalidSchema            = errors.New("data permission: invalid schema")
)

type subjectValueType uint8

const (
	subjectValueText subjectValueType = iota + 1
	subjectValueTextList
)

var trustedSubjectFields = map[string]subjectValueType{
	"id":             subjectValueText,
	"tenant_id":      subjectValueText,
	"membership_id":  subjectValueText,
	"role_codes":     subjectValueTextList,
	"department_ids": subjectValueTextList,
}

func subjectFieldType(name string) (subjectValueType, bool) {
	valueType, ok := trustedSubjectFields[name]
	return valueType, ok
}

// Effect identifies whether a matching data predicate adds or removes rows.
type Effect string

const (
	EffectAllow Effect = "allow"
	EffectDeny  Effect = "deny"
)

// PolicyScope is one already subject/resource/action-matched data policy.
type PolicyScope struct {
	Effect            Effect
	Predicate         Predicate
	ProposedPredicate Predicate
	HasProposed       bool
}

// SubjectAttributes contains trusted values resolved by the service boundary.
type SubjectAttributes map[string]any

type predicateKind uint8

const (
	predicateEmpty predicateKind = iota
	predicateEqual
	predicateIn
	predicateAnd
	predicateOr
	predicateNot
)

type operandKind uint8

const (
	operandResource operandKind = iota + 1
	operandProposed
	operandSubject
	operandLiteral
)

// Operand is a typed IR value reference created through the constructors below.
type Operand struct {
	kind    operandKind
	name    string
	literal any
}

// Predicate is the database-independent data-condition IR.
type Predicate struct {
	kind     predicateKind
	left     Operand
	right    Operand
	children []Predicate
}

// ResourceField references a logical field registered in a Resource Schema.
func ResourceField(name string) Operand { return Operand{kind: operandResource, name: name} }

// ProposedField references a logical field on a trusted, server-constructed
// target object. Proposed fields are evaluated in memory and never compiled to
// SQL identifiers or values.
func ProposedField(name string) Operand { return Operand{kind: operandProposed, name: name} }

// SubjectField references a trusted subject attribute bound at evaluation time.
func SubjectField(name string) Operand { return Operand{kind: operandSubject, name: name} }

// Literal creates a parameterized constant; it never becomes SQL text.
func Literal(value any) Operand { return Operand{kind: operandLiteral, literal: value} }

// Equal compares a ResourceField with a SubjectField or Literal.
func Equal(left, right Operand) Predicate {
	return Predicate{kind: predicateEqual, left: left, right: right}
}

// In checks whether a ResourceField belongs to a SubjectField collection.
func In(left, right Operand) Predicate {
	return Predicate{kind: predicateIn, left: left, right: right}
}

// And requires every child predicate to match.
func And(predicates ...Predicate) Predicate {
	return Predicate{kind: predicateAnd, children: append([]Predicate(nil), predicates...)}
}

// Or requires at least one child predicate to match.
func Or(predicates ...Predicate) Predicate {
	return Predicate{kind: predicateOr, children: append([]Predicate(nil), predicates...)}
}

// Not negates one child predicate.
func Not(predicate Predicate) Predicate {
	return Predicate{kind: predicateNot, children: []Predicate{predicate}}
}
