package routepolicy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadBootstrapManifestIsStrictAndBounded(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	validPath := filepath.Join(directory, "valid.yaml")
	require.NoError(t, os.WriteFile(validPath, []byte("version: 1\npolicies:\n  - protocol: http\n    method: post\n    path: /api/v1/route-policies/set\n    expression: authenticated\n    status: active\n"), 0o600))
	manifest, err := LoadBootstrapManifest(validPath)
	require.NoError(t, err)
	require.Len(t, manifest.Policies, 1)

	unknownPath := filepath.Join(directory, "unknown.yaml")
	require.NoError(t, os.WriteFile(unknownPath, []byte("version: 1\nunknown: true\npolicies: []\n"), 0o600))
	_, err = LoadBootstrapManifest(unknownPath)
	require.ErrorContains(t, err, "field unknown not found")
}

func TestBootstrapPolicyEqualIgnoresReferenceOrder(t *testing.T) {
	t.Parallel()
	existing := View{Record: Record{Expression: " authenticated ", Description: "manage", Status: "active"}, References: []Reference{{PermissionID: "p1", Scope: "platform"}, {PermissionID: "p2", Scope: "tenant"}}}
	desired := SetInput{Expression: "authenticated", Description: "manage", Status: "active", References: []ReferenceInput{{PermissionID: "p2", Scope: "tenant"}, {PermissionID: "p1", Scope: "platform"}}}
	require.True(t, bootstrapPolicyEqual(existing, desired))
}

func TestExampleBootstrapManifestUsesStablePermissionAndValidPolicies(t *testing.T) {
	t.Parallel()
	manifest, err := LoadBootstrapManifest(filepath.Join("..", "..", "config", "route-policies.bootstrap.example.yaml"))
	require.NoError(t, err)
	require.Len(t, manifest.PermissionDefinitions, 1)
	require.Equal(t, "platform.route-policy.manage", manifest.PermissionDefinitions[0].Key)
	compiler, err := NewCompiler()
	require.NoError(t, err)
	permission := Permission{ID: "permission-1", Key: "platform.route-policy.manage", Resource: "platform.route-policy", Action: "manage"}
	for _, policy := range manifest.Policies {
		_, err := compiler.Compile(Definition{ID: "policy", RouteID: "route", Expression: policy.Expression, Permissions: map[string]Permission{permission.Key: permission}, Version: 1})
		require.NoError(t, err)
	}
}
