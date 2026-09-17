package datapermission

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/lihongjie0209/go-api-template/internal/pbac"
)

var ErrSnapshotUnavailable = errors.New("data permission: snapshot unavailable")
var ErrObjectDenied = errors.New("data permission: object denied")

type snapshot struct{ policies map[string][]CompiledPolicy }

type Engine struct {
	schemas   *SchemaRegistry
	resources *pbac.Registry
	snapshot  atomic.Pointer[snapshot]
}

func NewEngine(schemas *SchemaRegistry, resources *pbac.Registry, policies []Policy) (*Engine, error) {
	engine := &Engine{schemas: schemas, resources: resources}
	if err := engine.Replace(policies); err != nil {
		return nil, err
	}
	return engine, nil
}

func NewRuntimeEngine(schemas *SchemaRegistry, resources *pbac.Registry) (*Engine, error) {
	return NewEngine(schemas, resources, nil)
}

func (e *Engine) Replace(policies []Policy) error {
	if e == nil || e.schemas == nil || e.resources == nil {
		return ErrSnapshotUnavailable
	}
	next := &snapshot{policies: make(map[string][]CompiledPolicy)}
	for _, policy := range policies {
		compiled, err := policy.Compile(e.schemas, e.resources)
		if err != nil {
			return fmt.Errorf("compile policy %q: %w", policy.Metadata.Code, err)
		}
		for _, action := range policy.Spec.Actions {
			key := policy.Spec.Resource + "\x00" + action
			next.policies[key] = append(next.policies[key], compiled)
		}
	}
	e.snapshot.Store(next)
	return nil
}

// CompileSQL matches policies and produces the fixed allow-union minus
// deny-union SQL scope for one already operation-authorized request.
func (e *Engine) CompileSQL(ctx context.Context, resource, action string, subject pbac.Subject, attributes SubjectAttributes) (SQLPredicate, error) {
	if err := ctx.Err(); err != nil {
		return SQLPredicate{}, err
	}
	if e == nil || e.schemas == nil || e.resources == nil {
		return SQLPredicate{}, ErrSnapshotUnavailable
	}
	schema, ok := e.schemas.Get(resource)
	if !ok {
		return SQLPredicate{}, ErrInvalidSchema
	}
	matched, err := e.matchedScopes(resource, action, subject)
	if err != nil {
		return SQLPredicate{}, err
	}
	return NewCompiler(schema).Compile(matched, attributes)
}

// EvaluateObject applies the same published policy snapshot to one trusted,
// server-constructed object before it is inserted. A create has no old row, so
// the constructed object is used for both current and proposed predicates.
func (e *Engine) EvaluateObject(ctx context.Context, resource, action string, subject pbac.Subject, subjectAttributes SubjectAttributes, resourceAttributes ResourceAttributes) (bool, error) {
	return e.EvaluateTransition(ctx, resource, action, subject, subjectAttributes, resourceAttributes, resourceAttributes)
}

// EvaluateTransition applies current-row and proposed-object predicates to a
// server-constructed state transition after the current row was selected under
// the SQL data scope.
func (e *Engine) EvaluateTransition(ctx context.Context, resource, action string, subject pbac.Subject, subjectAttributes SubjectAttributes, resourceAttributes, proposedAttributes ResourceAttributes) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if e == nil || e.schemas == nil || e.resources == nil {
		return false, ErrSnapshotUnavailable
	}
	schema, ok := e.schemas.Get(resource)
	if !ok {
		return false, ErrInvalidSchema
	}
	matched, err := e.matchedScopes(resource, action, subject)
	if err != nil {
		return false, err
	}
	return NewEvaluator(schema).EvaluateTransition(matched, subjectAttributes, resourceAttributes, proposedAttributes)
}

func (e *Engine) matchedScopes(resource, action string, subject pbac.Subject) ([]PolicyScope, error) {
	if e == nil || e.schemas == nil || e.resources == nil {
		return nil, ErrSnapshotUnavailable
	}
	current := e.snapshot.Load()
	if current == nil {
		return nil, ErrSnapshotUnavailable
	}
	matched := make([]PolicyScope, 0)
	for _, policy := range current.policies[resource+"\x00"+action] {
		if policy.Policy.Scope.Type == PolicyScopeTenant && policy.Policy.Scope.TenantID != subject.TenantID {
			continue
		}
		if !pbac.MatchSubject(policy.Policy.Spec.Subject, subject) {
			continue
		}
		matched = append(matched, PolicyScope{
			Effect: policy.Policy.Spec.Effect, Predicate: policy.Predicate, ProposedPredicate: policy.ProposedPredicate,
			HasProposed: policy.Policy.Spec.ProposedCondition != "",
		})
	}
	return matched, nil
}
