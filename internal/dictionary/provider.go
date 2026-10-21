package dictionary

import (
	"context"
	"sync"

	"github.com/lihongjie0209/go-api-template/internal/dependency"
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
	if r == nil || !codePattern.MatchString(code) || dependency.IsNil(provider) {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]Provider)
	}
	if _, ok := r.providers[code]; ok {
		return ErrConflict
	}
	r.providers[code] = provider
	return nil
}

func (r *ProviderRegistry) Has(code string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providers[code] != nil
}

func (r *ProviderRegistry) Query(ctx context.Context, query Query) (Result, error) {
	if r == nil {
		return Result{}, ErrProviderUnavailable
	}
	if err := validateQuery(query); err != nil {
		return Result{}, err
	}
	r.mu.RLock()
	provider := r.providers[query.Code]
	r.mu.RUnlock()
	if provider == nil {
		return Result{}, ErrNotFound
	}
	return provider.Query(ctx, query)
}
