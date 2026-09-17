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

// Simulator validates and evaluates candidate operation policies without
// persisting them or replacing the runtime engine snapshot.
type Simulator struct{ registry *Registry }

func NewSimulator(registry *Registry) *Simulator { return &Simulator{registry: registry} }

func (s *Simulator) Simulate(ctx context.Context, input SimulationInput) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	engine, err := NewEngine(s.registry, []Policy{input.Policy})
	if err != nil {
		return Decision{}, err
	}
	if err := validateSimulationTarget(s.registry, input); err != nil {
		return Decision{}, err
	}
	return engine.Evaluate(ctx, input.Request)
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
