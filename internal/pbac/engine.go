package pbac

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
)

var (
	ErrInvalidEvaluationRequest = errors.New("pbac: invalid evaluation request")
	ErrDuplicatePolicy          = errors.New("pbac: duplicate policy")
	ErrSnapshotUnavailable      = errors.New("pbac: policy snapshot unavailable")
)

type DecisionEffect string

const (
	DecisionEffectAllow         DecisionEffect = "allow"
	DecisionEffectDeny          DecisionEffect = "deny"
	DecisionEffectIndeterminate DecisionEffect = "indeterminate"
)

const (
	ReasonAllowed               = "PBAC_ALLOWED"
	ReasonExplicitDeny          = "PBAC_EXPLICIT_DENY"
	ReasonNoMatchingPolicy      = "PBAC_NO_MATCHING_POLICY"
	ReasonResourceActionUnknown = "PBAC_RESOURCE_ACTION_UNKNOWN"
	ReasonEvaluationFailed      = "PBAC_EVALUATION_FAILED"
	ReasonPolicyMissing         = "PBAC_POLICY_MISSING"
	ReasonTenantMismatch        = "PBAC_TENANT_MISMATCH"
)

// Decision explains the stable outcome without exposing condition internals.
type Decision struct {
	Effect           DecisionEffect `json:"effect"`
	ReasonCode       string         `json:"reason_code"`
	MatchedPolicies  []string       `json:"matched_policies"`
	DeniedByPolicyID string         `json:"denied_by_policy_id,omitempty"`
}

type compiledPolicy struct {
	policy Policy
	when   *compiledWhen
}

type policySnapshot struct {
	global   map[string][]*compiledPolicy
	tenant   map[string]map[string][]*compiledPolicy
	policies []Policy
}

// Engine evaluates immutable compiled snapshots and atomically replaces them
// only after every policy in a new set has validated and compiled.
type Engine struct {
	registry *Registry
	snapshot atomic.Pointer[policySnapshot]
}

// NewEngine creates an engine and installs the initial complete policy set.
func NewEngine(registry *Registry, policies []Policy) (*Engine, error) {
	if registry == nil {
		return nil, errors.New("pbac: resource-action registry is required")
	}
	engine := &Engine{registry: registry}
	if err := engine.Replace(policies); err != nil {
		return nil, err
	}
	return engine, nil
}

// Replace builds a complete next snapshot before atomically making it visible
// to concurrent evaluations.
func (e *Engine) Replace(policies []Policy) error {
	if e == nil || e.registry == nil {
		return ErrSnapshotUnavailable
	}
	next := &policySnapshot{
		global:   make(map[string][]*compiledPolicy),
		tenant:   make(map[string]map[string][]*compiledPolicy),
		policies: make([]Policy, 0, len(policies)),
	}
	identities := make(map[string]struct{}, len(policies))
	for _, source := range policies {
		policy := clonePolicy(source)
		if err := policy.Validate(e.registry); err != nil {
			return fmt.Errorf("validate policy %q: %w", policy.Metadata.Code, err)
		}
		identity := string(policy.Scope.Type) + "\x00" + policy.Scope.TenantID + "\x00" + policy.Metadata.Code
		if _, exists := identities[identity]; exists {
			return fmt.Errorf("%w: %q", ErrDuplicatePolicy, policy.Metadata.Code)
		}
		identities[identity] = struct{}{}
		condition, err := compileWhen(policy.Spec.When)
		if err != nil {
			return fmt.Errorf("compile policy %q when: %w", policy.Metadata.Code, err)
		}
		compiled := &compiledPolicy{policy: policy, when: condition}
		next.policies = append(next.policies, clonePolicy(policy))
		for _, action := range policy.Spec.Actions {
			key := candidateKey(policy.Spec.Resource.Type, action)
			if policy.Scope.Type == PolicyScopeGlobal {
				next.global[key] = append(next.global[key], compiled)
				continue
			}
			byKey := next.tenant[policy.Scope.TenantID]
			if byKey == nil {
				byKey = make(map[string][]*compiledPolicy)
				next.tenant[policy.Scope.TenantID] = byKey
			}
			byKey[key] = append(byKey[key], compiled)
		}
	}
	sortSnapshot(next)
	e.snapshot.Store(next)
	return nil
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
	policies := make([]Policy, 0, len(current.policies))
	for _, policy := range current.policies {
		policies = append(policies, clonePolicy(policy))
	}
	return policies, nil
}

// Evaluate applies structural matchers, trusted operation conditions, and the
// fixed deny-overrides combining algorithm. Row-level conditions belong
// exclusively to datapermission.
func (e *Engine) Evaluate(ctx context.Context, request EvaluationRequest) (Decision, error) {
	if e == nil || e.registry == nil {
		return indeterminate(ReasonEvaluationFailed), ErrSnapshotUnavailable
	}
	if err := ctx.Err(); err != nil {
		return indeterminate(ReasonEvaluationFailed), err
	}
	resourceDefinition, _, err := e.registry.Resolve(request.Resource.Type, request.Action)
	if err != nil {
		return indeterminate(ReasonResourceActionUnknown), fmt.Errorf("%w: %v", ErrInvalidEvaluationRequest, err)
	}
	if resourceDefinition.Scope == ResourceScopeTenant &&
		(request.Subject.TenantID == "" || request.Resource.TenantID == "" ||
			request.Subject.TenantID != request.Resource.TenantID) {
		return Decision{Effect: DecisionEffectDeny, ReasonCode: ReasonTenantMismatch}, nil
	}
	snapshot := e.snapshot.Load()
	if snapshot == nil {
		return indeterminate(ReasonEvaluationFailed), ErrSnapshotUnavailable
	}

	key := candidateKey(request.Resource.Type, request.Action)
	candidates := append([]*compiledPolicy(nil), snapshot.global[key]...)
	if request.Subject.TenantID != "" {
		candidates = append(candidates, snapshot.tenant[request.Subject.TenantID][key]...)
	}
	matchedAllow := false
	matched := make([]string, 0, len(candidates))
	deniedBy := make([]string, 0, 1)
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return indeterminate(ReasonEvaluationFailed), err
		}
		if !MatchSubject(candidate.policy.Spec.Subject, request.Subject) ||
			!MatchResource(candidate.policy.Spec.Resource, request.Resource) {
			continue
		}
		conditionMatched, conditionErr := candidate.when.evaluate(ctx, request)
		if conditionErr != nil {
			return indeterminate(ReasonEvaluationFailed), conditionErr
		}
		if !conditionMatched {
			continue
		}
		matched = append(matched, candidate.policy.Metadata.Code)
		if candidate.policy.Spec.Effect == EffectDeny {
			deniedBy = append(deniedBy, candidate.policy.Metadata.Code)
		} else {
			matchedAllow = true
		}
	}
	slices.Sort(matched)
	matched = slices.Compact(matched)
	if len(deniedBy) > 0 {
		slices.Sort(deniedBy)
		return Decision{
			Effect:           DecisionEffectDeny,
			ReasonCode:       ReasonExplicitDeny,
			MatchedPolicies:  matched,
			DeniedByPolicyID: deniedBy[0],
		}, nil
	}
	if matchedAllow {
		return Decision{Effect: DecisionEffectAllow, ReasonCode: ReasonAllowed, MatchedPolicies: matched}, nil
	}
	return Decision{Effect: DecisionEffectDeny, ReasonCode: ReasonNoMatchingPolicy}, nil
}

func indeterminate(reason string) Decision {
	return Decision{Effect: DecisionEffectIndeterminate, ReasonCode: reason}
}

func candidateKey(resource, action string) string { return resource + "\x00" + action }

func sortSnapshot(snapshot *policySnapshot) {
	sortPolicies := func(policies []*compiledPolicy) {
		slices.SortFunc(policies, func(left, right *compiledPolicy) int {
			return strings.Compare(left.policy.Metadata.Code, right.policy.Metadata.Code)
		})
	}
	for _, policies := range snapshot.global {
		sortPolicies(policies)
	}
	for _, byKey := range snapshot.tenant {
		for _, policies := range byKey {
			sortPolicies(policies)
		}
	}
}

func clonePolicy(policy Policy) Policy {
	policy.Spec.Actions = slices.Clone(policy.Spec.Actions)
	policy.Spec.Subject.Types = slices.Clone(policy.Spec.Subject.Types)
	policy.Spec.Subject.IDs = slices.Clone(policy.Spec.Subject.IDs)
	policy.Spec.Subject.Roles.AnyOf = slices.Clone(policy.Spec.Subject.Roles.AnyOf)
	policy.Spec.Subject.Roles.AllOf = slices.Clone(policy.Spec.Subject.Roles.AllOf)
	policy.Spec.Subject.Roles.NoneOf = slices.Clone(policy.Spec.Subject.Roles.NoneOf)
	return policy
}
