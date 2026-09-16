package pbac

import (
	"context"
	"fmt"
)

// NewRuntimeEngine creates an initially empty, deny-by-default engine. The
// authoritative published snapshot is loaded before transports start.
func NewRuntimeEngine(registry *Registry) (*Engine, error) {
	return NewEngine(registry, nil)
}

// RuntimeLoader rebuilds the complete engine snapshot from authoritative
// published database state.
type RuntimeLoader struct {
	repository *Repository
	engine     *Engine
	sync       runtimeSync
}

type runtimeSync interface {
	Refresh(context.Context) error
	Changed(context.Context) error
	Run(context.Context)
}

func NewRuntimeLoader(repository *Repository, engine *Engine) *RuntimeLoader {
	return &RuntimeLoader{repository: repository, engine: engine}
}

func (l *RuntimeLoader) ConfigureSync(sync runtimeSync) { l.sync = sync }

func (l *RuntimeLoader) Initialize(ctx context.Context) error {
	if l.sync != nil {
		return l.sync.Refresh(ctx)
	}
	return l.Refresh(ctx)
}

func (l *RuntimeLoader) Changed(ctx context.Context) error {
	if l.sync != nil {
		return l.sync.Changed(ctx)
	}
	return l.Refresh(ctx)
}

func (l *RuntimeLoader) Run(ctx context.Context) {
	if l.sync != nil {
		l.sync.Run(ctx)
	}
}

// Refresh preserves the previous engine snapshot unless every published policy
// loads, parses, validates, and compiles successfully.
func (l *RuntimeLoader) Refresh(ctx context.Context) error {
	if l == nil || l.repository == nil || l.engine == nil {
		return ErrSnapshotUnavailable
	}
	policies, err := l.repository.LoadAllPublished(ctx)
	if err != nil {
		return err
	}
	if err := l.engine.Replace(policies); err != nil {
		return fmt.Errorf("replace pbac runtime snapshot: %w", err)
	}
	return nil
}
