package presentation

import (
	"context"
	"errors"
	"strings"
)

const maxActorIDs = 1000

var ErrInvalidActorIDs = errors.New("invalid actor ids")

// ActorNames resolves identities through the owning service's interface.
// Missing, deleted, system and external identities deliberately fall back to
// their stable ID so response records never expose an opaque ID without a
// display field. A nil resolver is a supported deployment mode.
func ActorNames(ctx context.Context, resolver ActorResolver, ids ...string) (map[string]string, error) {
	names := make(map[string]string, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if len(id) > 256 {
			return nil, ErrInvalidActorIDs
		}
		if _, exists := names[id]; exists {
			continue
		}
		names[id] = id
		unique = append(unique, id)
		if len(unique) > maxActorIDs {
			return nil, ErrInvalidActorIDs
		}
	}
	if len(unique) == 0 || resolver == nil {
		return names, nil
	}
	resolved, err := resolver.ResolveUserIDs(ctx, unique)
	if err != nil {
		return nil, err
	}
	for id, name := range resolved {
		if _, requested := names[id]; requested && strings.TrimSpace(name) != "" {
			names[id] = name
		}
	}
	return names, nil
}
