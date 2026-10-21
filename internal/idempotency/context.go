package idempotency

import (
	"context"

	platformidempotency "github.com/lihongjie0209/microservice-platform-go/idempotency"
)

func Valid(key string) bool { return platformidempotency.Valid(key) }

// WithContext carries a previously validated key. Callers that require
// idempotency must reject invalid input before calling it; invalid keys are
// cleared here only to prevent accidental downstream propagation or retries.
func WithContext(ctx context.Context, key string) context.Context {
	if !Valid(key) {
		// Mask an inherited key too: an invalid override must never reuse it.
		key = ""
	}
	return platformidempotency.WithContext(ctx, key)
}

func FromContext(ctx context.Context) (string, bool) {
	key, ok := platformidempotency.FromContext(ctx)
	return key, ok && Valid(key)
}
