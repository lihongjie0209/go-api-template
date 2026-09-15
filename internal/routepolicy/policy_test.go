package routepolicy

import (
	"context"
	"errors"
	"testing"

	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type authorizerFunc func(context.Context, platformprincipal.Principal, platformauthz.Requirement) error

func (f authorizerFunc) Authorize(
	ctx context.Context,
	principal platformprincipal.Principal,
	requirement platformauthz.Requirement,
) error {
	return f(ctx, principal, requirement)
}

func TestCompilerEvaluatesDatabaseExpression(t *testing.T) {
	t.Parallel()
	compiler, err := NewCompiler()
	require.NoError(t, err)
	policy, err := compiler.Compile(Definition{
		ID:         "policy-1",
		RouteID:    "route-1",
		Expression: `authenticated && permissions["platform.user.page"]`,
		Permissions: map[string]Permission{
			"platform.user.page": {
				ID:       "permission-1",
				Key:      "platform.user.page",
				Resource: "platform.user",
				Action:   "page",
				Scope:    platformauthz.ScopePlatform,
			},
		},
	})
	require.NoError(t, err)

	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{
		ID:   "user-1",
		Type: platformprincipal.TypeUser,
	})
	require.NoError(t, policy.Evaluate(ctx, authorizerFunc(func(
		_ context.Context,
		_ platformprincipal.Principal,
		requirement platformauthz.Requirement,
	) error {
		require.Equal(t, "platform.user", requirement.Resource)
		require.Equal(t, "page", requirement.Action)
		return nil
	})))
	require.ErrorIs(t, policy.Evaluate(t.Context(), nil), ErrDenied)
}

func TestCompilerRejectsInvalidPolicies(t *testing.T) {
	t.Parallel()
	compiler, err := NewCompiler()
	require.NoError(t, err)

	tests := []struct {
		name       string
		expression string
	}{
		{name: "syntax error", expression: `authenticated &&`},
		{name: "non boolean", expression: `principal_type`},
		{name: "unknown permission", expression: `permissions["platform.user.delete"]`},
		{name: "unused permission reference", expression: `authenticated`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			permissions := map[string]Permission{}
			if test.name == "unused permission reference" {
				permissions["platform.user.page"] = Permission{Resource: "platform.user", Action: "page"}
			}
			_, err := compiler.Compile(Definition{Expression: test.expression, Permissions: permissions})
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestPermissionKeysAreUnique(t *testing.T) {
	t.Parallel()
	require.Equal(t, []string{"platform.user.page"}, PermissionKeys(
		`permissions["platform.user.page"] || permissions["platform.user.page"]`,
	))
}

func TestSnapshotReplacementIsAtomic(t *testing.T) {
	t.Parallel()
	compiler, err := NewCompiler()
	require.NoError(t, err)
	snapshot := NewSnapshot(compiler)
	require.NoError(t, snapshot.Replace([]Definition{{ID: "policy-1", RouteID: "route-1", Expression: "anonymous", Permissions: map[string]Permission{}}}))

	_, err = snapshot.Resolve("route-1")
	require.NoError(t, err)
	err = snapshot.Replace([]Definition{{ID: "broken", RouteID: "route-2", Expression: "missing_variable", Permissions: map[string]Permission{}}})
	require.ErrorIs(t, err, ErrInvalid)
	_, err = snapshot.Resolve("route-1")
	require.NoError(t, err)
	_, err = snapshot.Resolve("route-2")
	require.True(t, errors.Is(err, ErrMissing))
}
