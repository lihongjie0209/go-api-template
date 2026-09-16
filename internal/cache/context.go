package cache

import (
	"context"
	"time"
)

const afterCommitTimeout = 3 * time.Second

// AfterCommitContext preserves request-scoped values while detaching cache
// repair from client cancellation. The fixed upper bound prevents a failed
// cache backend from delaying a completed database mutation indefinitely.
func AfterCommitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), afterCommitTimeout)
}
