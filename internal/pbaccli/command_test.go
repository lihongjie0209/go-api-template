package pbaccli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/stretchr/testify/require"
)

func TestPublishCommandUsesCobraValidationAndMachineReadableOutput(t *testing.T) {
	t.Parallel()
	var received publishOptions
	command := newCommand(func(_ context.Context, options publishOptions) (pbac.Publication, error) {
		received = options
		return pbac.Publication{Policy: pbac.PolicyRecord{ID: "policy-1"}, Version: pbac.PolicyVersionRecord{ID: "version-1", VersionNumber: 1}}, nil
	})
	output := new(bytes.Buffer)
	command.SetOut(output)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"action-policy", "publish", "--actor", "bootstrap-admin", "--tenant-id", "tenant-1", "--file", "policy.yaml", "--env", "production"})
	require.NoError(t, command.ExecuteContext(t.Context()))
	require.Equal(t, "bootstrap-admin", received.actor)
	require.Equal(t, "tenant-1", received.tenantID)
	require.JSONEq(t, `{"Policy":{"id":"policy-1","code":"","name":"","description":"","scope":"","status":"","created_at":"0001-01-01T00:00:00Z","created_by":"","updated_at":"0001-01-01T00:00:00Z","updated_by":"","version":0},"Version":{"id":"version-1","policy_id":"","version_number":1,"document":"","status":"","created_at":"0001-01-01T00:00:00Z","created_by":"","updated_at":"0001-01-01T00:00:00Z","updated_by":"","version":0}}`, output.String())
}

func TestPublishCommandRequiresActorAndFile(t *testing.T) {
	t.Parallel()
	command := newCommand(func(context.Context, publishOptions) (pbac.Publication, error) {
		t.Fatal("runner must not execute")
		return pbac.Publication{}, nil
	})
	command.SetArgs([]string{"action-policy", "publish"})
	require.Error(t, command.ExecuteContext(t.Context()))
}

func TestValidIdentity(t *testing.T) {
	t.Parallel()
	require.True(t, validIdentity("bootstrap-admin"))
	require.False(t, validIdentity(""))
	require.False(t, validIdentity(" bootstrap-admin"))
	require.False(t, validIdentity("bootstrap\nadmin"))
}

func TestRunPublishRejectsTenantBoundaryMismatchBeforeOpeningDatabase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	document := `api_version: authorization.platform/v1
kind: ActionPolicy
metadata:
  code: tenant-bootstrap
  name: Tenant bootstrap
scope:
  type: tenant
  tenant_id: tenant-1
spec:
  subject:
    authenticated: true
  resource:
    type: tenant.member
  actions: [read]
  effect: allow
`
	require.NoError(t, os.WriteFile(path, []byte(document), 0o600))
	_, err := runPublish(t.Context(), publishOptions{actor: "operator", tenantID: "tenant-2", file: path})
	require.ErrorIs(t, err, pbac.ErrInvalidPolicy)
}
