package cache

import (
	"context"
	"time"

	"github.com/lihongjie0209/microservice-platform-go/distlock"
)

// Lock is an acquired distributed lock. Implementations must verify ownership
// when extending or releasing it.
type Lock = distlock.Mutex

// Locker coordinates work between service instances.
type Locker = distlock.Locker

var (
	ErrInvalidLock = distlock.ErrInvalid
	ErrLockLost    = distlock.ErrNotOwned
)

func WithLock(ctx context.Context, locker Locker, key string, ttl, retryDelay time.Duration, fn func(context.Context) error) error {
	return distlock.WithLock(ctx, locker, key, ttl, retryDelay, fn)
}

// TryWithLock attempts once, then renews an acquired lease for the callback.
// Contention returns (false, nil) without executing fn.
func TryWithLock(ctx context.Context, locker Locker, key string, ttl time.Duration, fn func(context.Context) error) (bool, error) {
	return distlock.TryWithLock(ctx, locker, key, ttl, fn)
}
