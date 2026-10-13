package authorization

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func testAuthorizer(t *testing.T) (*Authorizer, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	registry, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	return &Authorizer{db: sqlx.NewDb(db, "sqlmock"), registry: registry}, mock
}

func TestAuthorizerPlatformGrant(t *testing.T) {
	a, mock := testAuthorizer(t)
	p := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}
	r := platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopePlatform}
	mock.ExpectQuery(`SELECT count\(\*\) FROM platform_principal_permissions`).WithArgs("user", "user-1", "identity.user", "read").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	require.NoError(t, a.Authorize(t.Context(), p, r))
	mock.ExpectQuery(`SELECT count\(\*\) FROM platform_principal_permissions`).WithArgs("user", "user-1", "identity.user", "read").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	require.ErrorIs(t, a.Authorize(t.Context(), p, r), platformauthz.ErrDenied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerTenantGrantAndMemo(t *testing.T) {
	a, mock := testAuthorizer(t)
	p := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"}
	ctx := withAuthorizationMemo(t.Context())
	mock.ExpectQuery(`SELECT DISTINCT res.resource_key,act.action_key`).WithArgs("tenant-1", "member-1", "user-1").WillReturnRows(sqlmock.NewRows([]string{"resource_key", "action_key"}).AddRow("tenant.member", "read"))
	require.NoError(t, a.Authorize(ctx, p, platformauthz.Requirement{Resource: "tenant.member", Action: "read", Scope: platformauthz.ScopeTenant}))
	require.ErrorIs(t, a.Authorize(ctx, p, platformauthz.Requirement{Resource: "tenant.member", Action: "update", Scope: platformauthz.ScopeTenant}), platformauthz.ErrDenied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerFailsClosed(t *testing.T) {
	a, mock := testAuthorizer(t)
	p := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}
	require.ErrorIs(t, a.Authorize(t.Context(), p, platformauthz.Requirement{Resource: "unknown", Action: "read", Scope: platformauthz.ScopePlatform}), platformauthz.ErrDecisionUnavailable)
	require.ErrorIs(t, a.Authorize(t.Context(), p, platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopeTenant}), platformauthz.ErrDecisionUnavailable)
	mock.ExpectQuery(`SELECT count\(\*\) FROM platform_principal_permissions`).WillReturnError(errors.New("database unavailable"))
	require.ErrorIs(t, a.Authorize(t.Context(), p, platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopePlatform}), platformauthz.ErrDecisionUnavailable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerPrincipalScope(t *testing.T) {
	a, _ := testAuthorizer(t)
	r := platformauthz.Requirement{Resource: "identity.profile", Action: "read", Scope: platformauthz.ScopePrincipal}
	require.NoError(t, a.Authorize(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}, r))
	require.ErrorIs(t, a.Authorize(t.Context(), platformprincipal.Principal{}, r), platformauthz.ErrDenied)
}
