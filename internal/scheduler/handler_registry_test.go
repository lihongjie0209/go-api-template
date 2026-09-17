package scheduler

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandlerRegistry(t *testing.T) {
	t.Parallel()
	registry := NewHandlerRegistry(slog.Default())
	handler, err := registry.Resolve("system.sample")
	require.NoError(t, err)
	require.NoError(t, handler(context.Background(), nil))
	_, err = registry.Resolve("unknown")
	require.ErrorIs(t, err, ErrHandlerNotRegistered)
	require.Equal(t, "system.sample", registry.Definitions()[0].Key)
}
