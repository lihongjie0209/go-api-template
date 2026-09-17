package datapermission

import (
	"context"
	"errors"

	"github.com/lihongjie0209/go-api-template/internal/pbac"
)

var ErrInvalidSimulation = errors.New("data permission: invalid simulation")

type SimulationInput struct {
	Policy             Policy             `json:"policy"`
	Action             string             `json:"action"`
	Subject            pbac.Subject       `json:"subject"`
	SubjectAttributes  SubjectAttributes  `json:"subject_attributes"`
	ResourceAttributes ResourceAttributes `json:"resource_attributes"`
	ProposedAttributes ResourceAttributes `json:"proposed_attributes"`
}

type PredicatePreview struct {
	Clause         string `json:"clause"`
	ParameterCount int    `json:"parameter_count"`
}

type SimulationResult struct {
	CurrentAllowed    bool             `json:"current_allowed"`
	TransitionAllowed bool             `json:"transition_allowed"`
	SQL               PredicatePreview `json:"sql"`
}

type Simulator struct {
	schemas   *SchemaRegistry
	resources *pbac.Registry
}

func NewSimulator(schemas *SchemaRegistry, resources *pbac.Registry) *Simulator {
	return &Simulator{schemas: schemas, resources: resources}
}

func (s *Simulator) Simulate(ctx context.Context, input SimulationInput) (SimulationResult, error) {
	if input.Action == "" {
		return SimulationResult{}, ErrInvalidSimulation
	}
	engine, err := NewEngine(s.schemas, s.resources, []Policy{input.Policy})
	if err != nil {
		return SimulationResult{}, err
	}
	resource, action := input.Policy.Spec.Resource, input.Action
	predicate, err := engine.CompileSQL(ctx, resource, action, input.Subject, input.SubjectAttributes)
	if err != nil {
		return SimulationResult{}, err
	}
	current, err := engine.EvaluateCurrent(ctx, resource, action, input.Subject, input.SubjectAttributes, input.ResourceAttributes)
	if err != nil {
		return SimulationResult{}, err
	}
	proposed := input.ProposedAttributes
	if proposed == nil {
		proposed = input.ResourceAttributes
	}
	transition, err := engine.EvaluateTransition(ctx, resource, action, input.Subject, input.SubjectAttributes, input.ResourceAttributes, proposed)
	if err != nil {
		return SimulationResult{}, err
	}
	return SimulationResult{CurrentAllowed: current, TransitionAllowed: transition, SQL: PredicatePreview{Clause: predicate.Clause, ParameterCount: len(predicate.Args)}}, nil
}
