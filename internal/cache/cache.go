package cache

import (
	"context"
	"time"

	platformcache "github.com/lihongjie0209/microservice-platform-go/cache"
)

// ErrMiss is returned when a cache key does not exist.
var (
	ErrMiss          = platformcache.ErrMiss
	ErrInvalidKey    = platformcache.ErrInvalidKey
	ErrInvalidTTL    = platformcache.ErrInvalidTTL
	ErrValueTooLarge = platformcache.ErrValueTooLarge
	ErrUnavailable   = platformcache.ErrUnavailable
)

// Store is the backend-independent contract for distributed cache operations.
// Values are bytes so callers are free to choose their serialization format.
type Store = platformcache.Store

// GetJSON reads and decodes a JSON value from a Store.
func GetJSON[T any](ctx context.Context, store Store, key string) (T, error) {
	return platformcache.GetJSON[T](ctx, store, key)
}

// SetJSON encodes and stores a JSON value. A zero TTL means no expiration.
func SetJSON[T any](ctx context.Context, store Store, key string, value T, ttl time.Duration) error {
	return platformcache.SetJSON(ctx, store, key, value, ttl)
}
