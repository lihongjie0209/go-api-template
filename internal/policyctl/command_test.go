package policyctl

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/routepolicy"
	"github.com/stretchr/testify/require"
)

func TestBootstrapCommandWritesMachineReadableResult(t *testing.T) {
	t.Parallel()
	var received bootstrapOptions
	command := newCommand(func(_ context.Context, options bootstrapOptions) (routepolicy.BootstrapResult, error) {
		received = options
		return routepolicy.BootstrapResult{Created: 2, Unchanged: 1}, nil
	})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"bootstrap", "--manifest", "policies.yaml", "--actor", "deployment/bootstrap"})
	require.NoError(t, command.ExecuteContext(t.Context()))
	require.Equal(t, "policies.yaml", received.manifest)
	require.JSONEq(t, `{"created":2,"updated":0,"unchanged":1}`, output.String())
}

func TestBootstrapCommandReturnsRunnerErrorWithoutUsage(t *testing.T) {
	t.Parallel()
	command := newCommand(func(context.Context, bootstrapOptions) (routepolicy.BootstrapResult, error) {
		return routepolicy.BootstrapResult{}, errors.New("database unavailable")
	})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"bootstrap", "--manifest", "policies.yaml", "--actor", "deployment/bootstrap"})
	require.ErrorContains(t, command.ExecuteContext(t.Context()), "database unavailable")
	require.Empty(t, output.String())
}
