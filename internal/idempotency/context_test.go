package idempotency

import (
	"strings"
	"testing"

	platformidempotency "github.com/lihongjie0209/microservice-platform-go/idempotency"
	"github.com/stretchr/testify/require"
)

func TestContextIdempotencyKeyValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		key   string
		valid bool
	}{
		{name: "valid", key: "create-123", valid: true},
		{name: "empty", key: ""},
		{name: "too short", key: "short"},
		{name: "header injection", key: "create-123\r\nx-forged: true"},
		{name: "oversized", key: strings.Repeat("a", 129)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := WithContext(t.Context(), test.key)
			got, ok := FromContext(ctx)
			require.Equal(t, test.valid, ok)
			if test.valid {
				require.Equal(t, test.key, got)
			}
		})
	}
}

func TestInvalidIdempotencyKeyClearsInheritedKey(t *testing.T) {
	t.Parallel()
	ctx := WithContext(t.Context(), "create-123")
	ctx = WithContext(ctx, "invalid\nkey")
	_, ok := FromContext(ctx)
	require.False(t, ok)
}

func TestInvalidSDKKeyIsNotPropagated(t *testing.T) {
	t.Parallel()
	ctx := platformidempotency.WithContext(t.Context(), "invalid\nkey")
	_, ok := FromContext(ctx)
	require.False(t, ok)
}
