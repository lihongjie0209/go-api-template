package pbac

import "context"

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
	engine, err := NewEngine(s.registry, []Policy{input.Policy})
	if err != nil {
		return Decision{}, err
	}
	return engine.Evaluate(ctx, input.Request)
}
