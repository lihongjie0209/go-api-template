package dictionary

import (
	"context"
	"sync"
)

// Provider is the contract implemented by business modules that expose a
// dynamic dictionary. Providers are registered and invoked in-process by
// dictionary code; there is intentionally no remote-provider transport.
type Provider interface {
	Query(context.Context, Query) (Result, error)
}

type ProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{providers: map[string]Provider{}}
}
func (r *ProviderRegistry) Register(code string, provider Provider) error {
	if !codePattern.MatchString(code) || provider == nil {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[code]; ok {
		return ErrConflict
	}
	r.providers[code] = provider
	return nil
}
func (r *ProviderRegistry) Query(ctx context.Context, query Query) (Result, error) {
	r.mu.RLock()
	provider := r.providers[query.Code]
	r.mu.RUnlock()
	if provider == nil {
		return Result{}, ErrNotFound
	}
	if err := validateQuery(query); err != nil {
		return Result{}, err
	}
	return provider.Query(ctx, query)
}
