package datapermission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
)

var ErrSnapshotUnavailable = errors.New("data permission: snapshot unavailable")
var ErrObjectDenied = errors.New("data permission: object denied")

type snapshot struct {
	policies map[string][]CompiledPolicy
	sources  []Policy
	revision string
}

type pinnedSnapshotKey struct{}

type pinnedSnapshot struct {
	engine   *Engine
	snapshot *snapshot
}

type Engine struct {
	schemas   *SchemaRegistry
	resources *resourceaction.Registry
	snapshot  atomic.Pointer[snapshot]
}

func NewEngine(schemas *SchemaRegistry, resources *resourceaction.Registry, policies []Policy) (*Engine, error) {
	engine := &Engine{schemas: schemas, resources: resources}
	if err := engine.Replace(policies); err != nil {
		return nil, err
	}
	return engine, nil
}

func NewRuntimeEngine(schemas *SchemaRegistry, resources *resourceaction.Registry) (*Engine, error) {
	return NewEngine(schemas, resources, nil)
}

func (e *Engine) Replace(policies []Policy) error {
	if e == nil || e.schemas == nil || e.resources == nil {
		return ErrSnapshotUnavailable
	}
	next := &snapshot{policies: make(map[string][]CompiledPolicy), sources: make([]Policy, 0, len(policies))}
	for _, input := range policies {
		policy := clonePolicy(input)
		compiled, err := policy.Compile(e.schemas, e.resources)
		if err != nil {
			return fmt.Errorf("compile policy %q: %w", policy.Metadata.Code, err)
		}
		for _, action := range policy.Spec.Actions {
			key := policy.Spec.Resource + "\x00" + action
			next.policies[key] = append(next.policies[key], compiled)
		}
		next.sources = append(next.sources, policy)
	}
	documents := make([]string, 0, len(next.sources))
	for _, policy := range next.sources {
		encoded, err := json.Marshal(policy)
		if err != nil {
			return fmt.Errorf("serialize policy revision: %w", err)
		}
		documents = append(documents, string(encoded))
	}
	slices.Sort(documents)
	digest := sha256.Sum256([]byte("data-policies/v1\x00" + strings.Join(documents, "\x00")))
	next.revision = hex.EncodeToString(digest[:16])
	e.snapshot.Store(next)
	return nil
}

// Invalidate removes the active snapshot after an authoritative change cannot
// be reloaded. New decisions fail closed until a complete replacement succeeds.
// Requests that already pinned a snapshot retain their immutable view.
func (e *Engine) Invalidate() {
	if e != nil {
		e.snapshot.Store(nil)
	}
}

// Pin binds one immutable policy snapshot and its revision to a request. All
// subsequent evaluations using this Engine and Context observe that snapshot,
// even if a refresh publishes a new one concurrently.
func (e *Engine) Pin(ctx context.Context) (context.Context, string, error) {
	if err := ctx.Err(); err != nil {
		return ctx, "", err
	}
	if e == nil || e.schemas == nil || e.resources == nil {
		return ctx, "", ErrSnapshotUnavailable
	}
	current := e.snapshot.Load()
	if current == nil {
		return ctx, "", ErrSnapshotUnavailable
	}
	return context.WithValue(ctx, pinnedSnapshotKey{}, pinnedSnapshot{engine: e, snapshot: current}), current.revision, nil
}

// Policies returns an immutable copy of the currently active source policies.
func (e *Engine) Policies() ([]Policy, error) {
	if e == nil {
		return nil, ErrSnapshotUnavailable
	}
	current := e.snapshot.Load()
	if current == nil {
		return nil, ErrSnapshotUnavailable
	}
	policies := make([]Policy, 0, len(current.sources))
	for _, policy := range current.sources {
		policies = append(policies, clonePolicy(policy))
	}
	return policies, nil
}

// CompileSQL matches policies and produces the fixed allow-union minus
// deny-union SQL scope for one already operation-authorized request.
func (e *Engine) CompileSQL(ctx context.Context, resource, action string, subject resourceaction.Subject, attributes SubjectAttributes) (SQLPredicate, error) {
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
	matched, err := e.matchedScopes(ctx, resource, action, subject)
	if err != nil {
		return SQLPredicate{}, err
	}
	return NewCompiler(schema).Compile(matched, attributes)
}

// EvaluateObject applies the same published policy snapshot to one trusted,
// server-constructed object before it is inserted. A create has no old row, so
// the constructed object is used for both current and proposed predicates.
func (e *Engine) EvaluateObject(ctx context.Context, resource, action string, subject resourceaction.Subject, subjectAttributes SubjectAttributes, resourceAttributes ResourceAttributes) (bool, error) {
	return e.EvaluateTransition(ctx, resource, action, subject, subjectAttributes, resourceAttributes, resourceAttributes)
}

// EvaluateCurrent evaluates only the persisted-row predicate. Proposed-state
// predicates are intentionally ignored because a generic row capability has no
// validated target state; the mutation endpoint must evaluate the transition.
func (e *Engine) EvaluateCurrent(ctx context.Context, resource, action string, subject resourceaction.Subject, subjectAttributes SubjectAttributes, resourceAttributes ResourceAttributes) (bool, error) {
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
	matched, err := e.matchedScopes(ctx, resource, action, subject)
	if err != nil {
		return false, err
	}
	return NewEvaluator(schema).Evaluate(matched, subjectAttributes, resourceAttributes)
}

// EvaluateTransition applies current-row and proposed-object predicates to a
// server-constructed state transition after the current row was selected under
// the SQL data scope.
func (e *Engine) EvaluateTransition(ctx context.Context, resource, action string, subject resourceaction.Subject, subjectAttributes SubjectAttributes, resourceAttributes, proposedAttributes ResourceAttributes) (bool, error) {
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
	matched, err := e.matchedScopes(ctx, resource, action, subject)
	if err != nil {
		return false, err
	}
	return NewEvaluator(schema).EvaluateTransition(matched, subjectAttributes, resourceAttributes, proposedAttributes)
}

func (e *Engine) matchedScopes(ctx context.Context, resource, action string, subject resourceaction.Subject) ([]PolicyScope, error) {
	if e == nil || e.schemas == nil || e.resources == nil {
		return nil, ErrSnapshotUnavailable
	}
	current := e.snapshot.Load()
	if pinned, ok := ctx.Value(pinnedSnapshotKey{}).(pinnedSnapshot); ok {
		if pinned.engine != e {
			return nil, ErrSnapshotUnavailable
		}
		current = pinned.snapshot
	}
	if current == nil {
		return nil, ErrSnapshotUnavailable
	}
	matched := make([]PolicyScope, 0)
	for _, policy := range current.policies[resource+"\x00"+action] {
		if policy.Policy.Scope.Type == PolicyScopeTenant && policy.Policy.Scope.TenantID != subject.TenantID {
			continue
		}
		if !resourceaction.MatchSubject(policy.Policy.Spec.Subject, subject) {
			continue
		}
		matched = append(matched, PolicyScope{
			Effect: policy.Policy.Spec.Effect, Predicate: policy.Predicate, ProposedPredicate: policy.ProposedPredicate,
			HasProposed: policy.Policy.Spec.ProposedCondition != "",
		})
	}
	return matched, nil
}
