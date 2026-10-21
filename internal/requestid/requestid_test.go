package requestid

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithContextUsesOnlyValidRequestIDs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		id   string
		keep bool
	}{
		{name: "valid", id: "request-123_A.b", keep: true},
		{name: "empty", id: ""},
		{name: "header injection", id: "request-1\r\nx-forged: true"},
		{name: "spaces", id: "request 1"},
		{name: "oversized", id: strings.Repeat("a", 129)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			parent, cancel := context.WithCancel(t.Context())
			ctx := WithContext(parent, test.id)
			id, ok := FromContext(ctx)
			require.True(t, ok)
			require.True(t, Valid(id))
			if test.keep {
				require.Equal(t, test.id, id)
			} else {
				require.NotEqual(t, test.id, id)
			}
			cancel()
			require.ErrorIs(t, ctx.Err(), context.Canceled)
		})
	}
}

func TestFromContextRejectsInvalidStoredRequestID(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(t.Context(), contextKey{}, "bad\nrequest-id")
	_, ok := FromContext(ctx)
	require.False(t, ok)
}
