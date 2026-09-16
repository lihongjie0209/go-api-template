package routepolicy

import (
	"context"
	"fmt"
	"sync/atomic"
)

type Loader interface {
	Load(context.Context) ([]Definition, error)
}

type Snapshot struct {
	compiler *Compiler
	value    atomic.Pointer[map[string]*compiled]
}

func (s *Snapshot) upsert(definition Definition) error {
	if definition.RouteID == "" {
		return fmt.Errorf("%w: empty route id", ErrInvalid)
	}
	policy, err := s.compiler.Compile(definition)
	if err != nil {
		return fmt.Errorf("compile policy %q: %w", definition.ID, err)
	}
	s.store(policy)
	return nil
}

func (s *Snapshot) store(policy *compiled) {
	current := *s.value.Load()
	next := make(map[string]*compiled, len(current)+1)
	for routeID, existing := range current {
		next[routeID] = existing
	}
	next[policy.definition.RouteID] = policy
	s.value.Store(&next)
}

func (s *Snapshot) delete(routeID string) {
	current := *s.value.Load()
	next := make(map[string]*compiled, len(current))
	for id, existing := range current {
		if id != routeID {
			next[id] = existing
		}
	}
	s.value.Store(&next)
}

func (s *Snapshot) clear() {
	empty := map[string]*compiled{}
	s.value.Store(&empty)
}

func NewSnapshot(compiler *Compiler) *Snapshot {
	snapshot := &Snapshot{compiler: compiler}
	empty := map[string]*compiled{}
	snapshot.value.Store(&empty)
	return snapshot
}

func (s *Snapshot) Replace(definitions []Definition) error {
	next := make(map[string]*compiled, len(definitions))
	for _, definition := range definitions {
		if definition.RouteID == "" {
			return fmt.Errorf("%w: empty route id", ErrInvalid)
		}
		policy, err := s.compiler.Compile(definition)
		if err != nil {
			return fmt.Errorf("compile policy %q: %w", definition.ID, err)
		}
		if _, exists := next[definition.RouteID]; exists {
			return fmt.Errorf("%w: duplicate policy for route %q", ErrInvalid, definition.RouteID)
		}
		next[definition.RouteID] = policy
	}
	s.value.Store(&next)
	return nil
}

func (s *Snapshot) Reload(ctx context.Context, loader Loader) error {
	definitions, err := loader.Load(ctx)
	if err != nil {
		return fmt.Errorf("load route policies: %w", err)
	}
	return s.Replace(definitions)
}

func (s *Snapshot) Resolve(routeID string) (*compiled, error) {
	policy, ok := (*s.value.Load())[routeID]
	if !ok {
		return nil, fmt.Errorf("%w: route %q", ErrMissing, routeID)
	}
	return policy, nil
}
