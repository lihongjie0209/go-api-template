package pbac

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

var ErrInvalidSimulation = errors.New("pbac: invalid simulation")

// SimulationInput evaluates one candidate policy in an isolated snapshot.
type SimulationInput struct {
	Policy  Policy            `json:"policy"`
	Request EvaluationRequest `json:"request"`
}

// SimulationResult compares the active snapshot with the effective snapshot
// produced by replacing a policy of the same identity with the candidate.
type SimulationResult struct {
	Baseline  Decision `json:"baseline"`
	Candidate Decision `json:"candidate"`
	Changed   bool     `json:"changed"`
}

// Simulator validates and evaluates candidate operation policies without
// persisting them or replacing the runtime engine snapshot.
type Simulator struct {
	registry *Registry
	runtime  *Engine
}

func NewSimulator(registry *Registry, runtime *Engine) *Simulator {
	return &Simulator{registry: registry, runtime: runtime}
}

func (s *Simulator) Simulate(ctx context.Context, input SimulationInput) (SimulationResult, error) {
	if err := ctx.Err(); err != nil {
		return SimulationResult{}, err
	}
	if err := validateSimulationTarget(s.registry, input); err != nil {
		return SimulationResult{}, err
	}
	policies, err := s.runtime.Policies()
	if err != nil {
		return SimulationResult{}, err
	}
	baselineEngine, err := NewEngine(s.registry, policies)
	if err != nil {
		return SimulationResult{}, err
	}
	baseline, err := baselineEngine.Evaluate(ctx, input.Request)
	if err != nil {
		return SimulationResult{}, err
	}
	engine, err := NewEngine(s.registry, replacePolicy(policies, input.Policy))
	if err != nil {
		return SimulationResult{}, err
	}
	candidate, err := engine.Evaluate(ctx, input.Request)
	if err != nil {
		return SimulationResult{}, err
	}
	return SimulationResult{Baseline: baseline, Candidate: candidate, Changed: !equalDecision(baseline, candidate)}, nil
}

func replacePolicy(policies []Policy, candidate Policy) []Policy {
	result := make([]Policy, 0, len(policies)+1)
	replaced := false
	for _, policy := range policies {
		if policy.Scope.Type == candidate.Scope.Type && policy.Scope.TenantID == candidate.Scope.TenantID &&
			policy.Metadata.Code == candidate.Metadata.Code {
			result = append(result, candidate)
			replaced = true
			continue
		}
		result = append(result, policy)
	}
	if replaced {
		return result
	}
	return append(result, candidate)
}

func equalDecision(left, right Decision) bool {
	return left.Effect == right.Effect && left.ReasonCode == right.ReasonCode &&
		slices.Equal(left.MatchedPolicies, right.MatchedPolicies) && left.DeniedByPolicyID == right.DeniedByPolicyID
}

func validateSimulationTarget(registry *Registry, input SimulationInput) error {
	request := input.Request
	if request.Resource.Type != input.Policy.Spec.Resource.Type ||
		!slices.Contains(input.Policy.Spec.Actions, request.Action) {
		return fmt.Errorf("%w: resource and action must be declared by the candidate policy", ErrInvalidSimulation)
	}
	definition, _, err := registry.Resolve(request.Resource.Type, request.Action)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSimulation, err)
	}
	if definition.Scope != ResourceScopeTenant {
		return nil
	}
	if request.Subject.TenantID == "" || request.Resource.TenantID == "" ||
		request.Subject.TenantID != request.Resource.TenantID {
		return fmt.Errorf("%w: subject and resource tenant must match", ErrInvalidSimulation)
	}
	if input.Policy.Scope.Type == PolicyScopeTenant && input.Policy.Scope.TenantID != request.Resource.TenantID {
		return fmt.Errorf("%w: candidate policy and resource tenant must match", ErrInvalidSimulation)
	}
	return nil
}
