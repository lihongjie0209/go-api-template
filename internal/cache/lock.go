package cache

import (
	"context"
	"log/slog"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/observability"
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

// WithCommittedLock is for callbacks whose nil return proves an authoritative
// transaction has committed. A subsequent lease-release error is observable but
// must not turn that committed business operation into a failed API response.
// Callbacks must not return nil before their final durable commit succeeds.
func WithCommittedLock(ctx context.Context, locker Locker, key string, ttl, retryDelay time.Duration, fn func(context.Context) error) error {
	if fn == nil {
		return WithLock(ctx, locker, key, ttl, retryDelay, nil)
	}
	committed := false
	err := WithLock(ctx, locker, key, ttl, retryDelay, func(leaseCtx context.Context) error {
		if err := fn(leaseCtx); err != nil {
			return err
		}
		committed = true
		return nil
	})
	if !committed {
		return err
	}
	if err != nil {
		slog.WarnContext(ctx, "distributed lock error after committed mutation", slog.Any("error", observability.SafeError(err, errLockOperationFailed)))
	}
	return nil
}

// TryWithLock attempts once, then renews an acquired lease for the callback.
// Contention returns (false, nil) without executing fn.
func TryWithLock(ctx context.Context, locker Locker, key string, ttl time.Duration, fn func(context.Context) error) (bool, error) {
	return distlock.TryWithLock(ctx, locker, key, ttl, fn)
}
