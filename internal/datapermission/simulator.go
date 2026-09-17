package datapermission

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/lihongjie0209/go-api-template/internal/pbac"
)

var ErrInvalidSimulation = errors.New("data permission: invalid simulation")

type SimulationInput struct {
	Policy             Policy             `json:"policy"`
	Action             string             `json:"action"`
	Subject            pbac.Subject       `json:"subject"`
	Resource           pbac.Resource      `json:"resource"`
	SubjectAttributes  SubjectAttributes  `json:"subject_attributes"`
	ResourceAttributes ResourceAttributes `json:"resource_attributes"`
	ProposedAttributes ResourceAttributes `json:"proposed_attributes"`
}

type PredicatePreview struct {
	Clause         string `json:"clause"`
	ParameterCount int    `json:"parameter_count"`
}

type EvaluationResult struct {
	CurrentAllowed    bool             `json:"current_allowed"`
	TransitionAllowed bool             `json:"transition_allowed"`
	SQL               PredicatePreview `json:"sql"`
}

type SimulationResult struct {
	Baseline  EvaluationResult `json:"baseline"`
	Candidate EvaluationResult `json:"candidate"`
	Changed   bool             `json:"changed"`
}

type Simulator struct {
	schemas   *SchemaRegistry
	resources *pbac.Registry
	runtime   *Engine
}

func NewSimulator(schemas *SchemaRegistry, resources *pbac.Registry, runtime *Engine) *Simulator {
	return &Simulator{schemas: schemas, resources: resources, runtime: runtime}
}

func (s *Simulator) Simulate(ctx context.Context, input SimulationInput) (SimulationResult, error) {
	if err := ctx.Err(); err != nil {
		return SimulationResult{}, err
	}
	if err := validateSimulationTarget(s.resources, input); err != nil {
		return SimulationResult{}, err
	}
	policies, err := s.runtime.Policies()
	if err != nil {
		return SimulationResult{}, err
	}
	baselineEngine, err := NewEngine(s.schemas, s.resources, policies)
	if err != nil {
		return SimulationResult{}, err
	}
	baseline, baselinePredicate, err := evaluateSimulation(ctx, baselineEngine, input)
	if err != nil {
		return SimulationResult{}, err
	}
	engine, err := NewEngine(s.schemas, s.resources, replacePolicy(policies, input.Policy))
	if err != nil {
		return SimulationResult{}, err
	}
	candidate, candidatePredicate, err := evaluateSimulation(ctx, engine, input)
	if err != nil {
		return SimulationResult{}, err
	}
	changed := baseline.CurrentAllowed != candidate.CurrentAllowed ||
		baseline.TransitionAllowed != candidate.TransitionAllowed ||
		baselinePredicate.Clause != candidatePredicate.Clause ||
		!reflect.DeepEqual(baselinePredicate.Args, candidatePredicate.Args)
	return SimulationResult{Baseline: baseline, Candidate: candidate, Changed: changed}, nil
}

func evaluateSimulation(ctx context.Context, engine *Engine, input SimulationInput) (EvaluationResult, SQLPredicate, error) {
	resource, action := input.Resource.Type, input.Action
	predicate, err := engine.CompileSQL(ctx, resource, action, input.Subject, input.SubjectAttributes)
	if err != nil {
		return EvaluationResult{}, SQLPredicate{}, err
	}
	current, err := engine.EvaluateCurrent(ctx, resource, action, input.Subject, input.SubjectAttributes, input.ResourceAttributes)
	if err != nil {
		return EvaluationResult{}, SQLPredicate{}, err
	}
	proposed := input.ProposedAttributes
	if proposed == nil {
		proposed = input.ResourceAttributes
	}
	transition, err := engine.EvaluateTransition(ctx, resource, action, input.Subject, input.SubjectAttributes, input.ResourceAttributes, proposed)
	if err != nil {
		return EvaluationResult{}, SQLPredicate{}, err
	}
	return EvaluationResult{CurrentAllowed: current, TransitionAllowed: transition, SQL: PredicatePreview{Clause: predicate.Clause, ParameterCount: len(predicate.Args)}}, predicate, nil
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

func validateSimulationTarget(resources *pbac.Registry, input SimulationInput) error {
	if input.Action == "" || input.Resource.Type != input.Policy.Spec.Resource ||
		!slices.Contains(input.Policy.Spec.Actions, input.Action) {
		return fmt.Errorf("%w: resource and action must be declared by the candidate policy", ErrInvalidSimulation)
	}
	definition, _, err := resources.Resolve(input.Resource.Type, input.Action)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSimulation, err)
	}
	if definition.Scope != pbac.ResourceScopeTenant {
		return nil
	}
	if input.Subject.TenantID == "" || input.Resource.TenantID == "" ||
		input.Subject.TenantID != input.Resource.TenantID {
		return fmt.Errorf("%w: subject and resource tenant must match", ErrInvalidSimulation)
	}
	if input.Policy.Scope.Type == PolicyScopeTenant && input.Policy.Scope.TenantID != input.Resource.TenantID {
		return fmt.Errorf("%w: candidate policy and resource tenant must match", ErrInvalidSimulation)
	}
	return nil
}
