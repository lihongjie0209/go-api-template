package authorization

import (
	"context"
	"errors"
	"fmt"
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
	mock.ExpectQuery(`SELECT 1 FROM platform_principal_permissions.*LIMIT 1`).WithArgs("user", "user-1", "identity.user", "read").WillReturnRows(sqlmock.NewRows([]string{"one"}).AddRow(1))
	require.NoError(t, a.Authorize(t.Context(), p, r))
	mock.ExpectQuery(`SELECT 1 FROM platform_principal_permissions.*LIMIT 1`).WithArgs("user", "user-1", "identity.user", "read").WillReturnRows(sqlmock.NewRows([]string{"one"}))
	require.ErrorIs(t, a.Authorize(t.Context(), p, r), platformauthz.ErrDenied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerTenantGrantSnapshot(t *testing.T) {
	a, mock := testAuthorizer(t)
	p := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT DISTINCT res.resource_key,act.action_key FROM platform_principal_permissions`).WithArgs("user", "user-1").WillReturnRows(sqlmock.NewRows([]string{"resource_key", "action_key"}))
	mock.ExpectQuery(`SELECT DISTINCT res.resource_key,act.action_key`).WithArgs("tenant-1", "member-1", "user-1").WillReturnRows(sqlmock.NewRows([]string{"resource_key", "action_key"}).AddRow("tenant.member", "read"))
	mock.ExpectCommit()
	ctx, _, err := a.Snapshot(t.Context(), p)
	require.NoError(t, err)
	require.NoError(t, a.Authorize(ctx, p, platformauthz.Requirement{Resource: "tenant.member", Action: "read", Scope: platformauthz.ScopeTenant}))
	require.ErrorIs(t, a.Authorize(ctx, p, platformauthz.Requirement{Resource: "tenant.member", Action: "update", Scope: platformauthz.ScopeTenant}), platformauthz.ErrDenied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerTenantGrantChecksOnlyRequestedAction(t *testing.T) {
	a, mock := testAuthorizer(t)
	principal := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"}
	requirement := platformauthz.Requirement{Resource: "tenant.member", Action: "read", Scope: platformauthz.ScopeTenant}
	mock.ExpectQuery(`SELECT 1 FROM permissions p.*res.resource_key=\? AND act.action_key=\? LIMIT 1`).
		WithArgs("tenant-1", "member-1", "user-1", "tenant.member", "read").
		WillReturnRows(sqlmock.NewRows([]string{"one"}).AddRow(1))
	require.NoError(t, a.Authorize(t.Context(), principal, requirement))
	mock.ExpectQuery(`SELECT 1 FROM permissions p.*res.resource_key=\? AND act.action_key=\? LIMIT 1`).
		WithArgs("tenant-1", "member-1", "user-1", "tenant.member", "read").
		WillReturnRows(sqlmock.NewRows([]string{"one"}))
	require.ErrorIs(t, a.Authorize(t.Context(), principal, requirement), platformauthz.ErrDenied)
	mock.ExpectQuery(`SELECT 1 FROM permissions p.*res.resource_key=\? AND act.action_key=\? LIMIT 1`).
		WithArgs("tenant-1", "member-1", "user-1", "tenant.member", "read").
		WillReturnError(errors.New("database unavailable"))
	require.ErrorIs(t, a.Authorize(t.Context(), principal, requirement), platformauthz.ErrDecisionUnavailable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerFailsClosed(t *testing.T) {
	a, mock := testAuthorizer(t)
	p := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}
	require.ErrorIs(t, a.Authorize(t.Context(), p, platformauthz.Requirement{Resource: "unknown", Action: "read", Scope: platformauthz.ScopePlatform}), platformauthz.ErrDecisionUnavailable)
	require.ErrorIs(t, a.Authorize(t.Context(), p, platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopeTenant}), platformauthz.ErrDecisionUnavailable)
	mock.ExpectQuery(`SELECT 1 FROM platform_principal_permissions.*LIMIT 1`).WillReturnError(errors.New("database unavailable"))
	require.ErrorIs(t, a.Authorize(t.Context(), p, platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopePlatform}), platformauthz.ErrDecisionUnavailable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerPrincipalScope(t *testing.T) {
	a, _ := testAuthorizer(t)
	r := platformauthz.Requirement{Resource: "identity.profile", Action: "read", Scope: platformauthz.ScopePrincipal}
	require.NoError(t, a.Authorize(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}, r))
	require.ErrorIs(t, a.Authorize(t.Context(), platformprincipal.Principal{}, r), platformauthz.ErrDenied)
}

func TestAuthorizerCanceledContextFailsClosed(t *testing.T) {
	a, mock := testAuthorizer(t)
	principal := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), grantSnapshotKey{}, grantSnapshot{
		principal: principal,
		platform:  map[string]struct{}{"identity.user\x00read": {}},
	}))
	cancel()
	require.ErrorIs(t, a.Authorize(ctx, principal, platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopePlatform}), platformauthz.ErrDecisionUnavailable)
	require.ErrorIs(t, a.Authorize(ctx, principal, platformauthz.Requirement{Resource: "identity.profile", Action: "read", Scope: platformauthz.ScopePrincipal}), platformauthz.ErrDecisionUnavailable)
	system := platformprincipal.Principal{ID: "system", Type: platformprincipal.TypeSystem}
	require.ErrorIs(t, a.Authorize(ctx, system, platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopePlatform}), platformauthz.ErrDecisionUnavailable)
	_, revision, err := a.Snapshot(ctx, system)
	require.ErrorIs(t, err, platformauthz.ErrDecisionUnavailable)
	require.Empty(t, revision)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerSnapshotTracksPlatformGrantsAndReusesSnapshot(t *testing.T) {
	a, mock := testAuthorizer(t)
	principal := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT DISTINCT res.resource_key,act.action_key FROM platform_principal_permissions`).
		WithArgs("user", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"resource_key", "action_key"}).AddRow("identity.user", "read"))
	mock.ExpectCommit()
	ctx, revision, err := a.Snapshot(t.Context(), principal)
	require.NoError(t, err)
	require.NotEmpty(t, revision)
	require.NoError(t, a.Authorize(ctx, principal, platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopePlatform}))
	require.ErrorIs(t, a.Authorize(ctx, principal, platformauthz.Requirement{Resource: "identity.user", Action: "update", Scope: platformauthz.ScopePlatform}), platformauthz.ErrDenied)
	require.NoError(t, mock.ExpectationsWereMet())

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT DISTINCT res.resource_key,act.action_key FROM platform_principal_permissions`).
		WithArgs("user", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"resource_key", "action_key"}).AddRow("identity.user", "update"))
	mock.ExpectCommit()
	_, changed, err := a.Snapshot(t.Context(), principal)
	require.NoError(t, err)
	require.NotEqual(t, revision, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerSnapshotTracksTenantGrants(t *testing.T) {
	a, mock := testAuthorizer(t)
	principal := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"}
	var firstRevision string
	for _, action := range []string{"read", "update"} {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT DISTINCT res.resource_key,act.action_key FROM platform_principal_permissions`).
			WithArgs("user", "user-1").
			WillReturnRows(sqlmock.NewRows([]string{"resource_key", "action_key"}))
		mock.ExpectQuery(`SELECT DISTINCT res.resource_key,act.action_key FROM permissions`).
			WithArgs("tenant-1", "member-1", "user-1").
			WillReturnRows(sqlmock.NewRows([]string{"resource_key", "action_key"}).AddRow("tenant.member", action))
		mock.ExpectCommit()
		_, revision, err := a.Snapshot(t.Context(), principal)
		require.NoError(t, err)
		if action == "read" {
			firstRevision = revision
		} else {
			require.NotEqual(t, firstRevision, revision)
		}
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerSnapshotIsBoundToPrincipalAndIndependentOfRowOrder(t *testing.T) {
	a, mock := testAuthorizer(t)
	principal := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT DISTINCT res.resource_key,act.action_key FROM platform_principal_permissions`).
		WithArgs("user", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"resource_key", "action_key"}).AddRow("identity.user", "read").AddRow("identity.user", "update"))
	mock.ExpectCommit()
	ctx, first, err := a.Snapshot(t.Context(), principal)
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT DISTINCT res.resource_key,act.action_key FROM platform_principal_permissions`).
		WithArgs("user", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"resource_key", "action_key"}).AddRow("identity.user", "update").AddRow("identity.user", "read"))
	mock.ExpectCommit()
	_, second, err := a.Snapshot(t.Context(), principal)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.ErrorIs(t, a.Authorize(ctx, platformprincipal.Principal{ID: "user-2", Type: platformprincipal.TypeUser}, platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopePlatform}), platformauthz.ErrDecisionUnavailable)
	otherSession := principal
	otherSession.SessionID = "session-2"
	require.ErrorIs(t, a.Authorize(ctx, otherSession, platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopePlatform}), platformauthz.ErrDecisionUnavailable)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT DISTINCT res.resource_key,act.action_key FROM platform_principal_permissions`).
		WithArgs("user", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"resource_key", "action_key"}).AddRow("identity.user", "read").AddRow("identity.user", "update"))
	mock.ExpectCommit()
	_, otherSessionRevision, err := a.Snapshot(t.Context(), otherSession)
	require.NoError(t, err)
	require.NotEqual(t, first, otherSessionRevision)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerSnapshotFailsClosedWhenDatabaseIsUnavailable(t *testing.T) {
	a, mock := testAuthorizer(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT DISTINCT res.resource_key,act.action_key FROM platform_principal_permissions`).
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()
	_, _, err := a.Snapshot(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	require.ErrorIs(t, err, platformauthz.ErrDecisionUnavailable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerSnapshotFailureInvalidatesPriorGrantContext(t *testing.T) {
	a, mock := testAuthorizer(t)
	principal := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}
	requirement := platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopePlatform}
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM platform_principal_permissions`).
		WillReturnRows(sqlmock.NewRows([]string{"resource_key", "action_key"}).AddRow("identity.user", "read"))
	mock.ExpectCommit()
	priorCtx, _, err := a.Snapshot(t.Context(), principal)
	require.NoError(t, err)
	require.NoError(t, a.Authorize(priorCtx, principal, requirement))

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM platform_principal_permissions`).WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()
	failedCtx, revision, err := a.Snapshot(priorCtx, principal)
	require.ErrorIs(t, err, platformauthz.ErrDecisionUnavailable)
	require.Empty(t, revision)
	require.ErrorIs(t, a.Authorize(failedCtx, principal, requirement), platformauthz.ErrDecisionUnavailable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerSnapshotFailsClosedOnTenantReadAndTransactionCommit(t *testing.T) {
	a, mock := testAuthorizer(t)
	principal := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT DISTINCT res.resource_key,act.action_key FROM platform_principal_permissions`).
		WillReturnRows(sqlmock.NewRows([]string{"resource_key", "action_key"}).AddRow("identity.user", "read"))
	mock.ExpectQuery(`SELECT DISTINCT res.resource_key,act.action_key FROM permissions`).
		WillReturnError(errors.New("tenant grants unavailable"))
	mock.ExpectRollback()
	_, revision, err := a.Snapshot(t.Context(), principal)
	require.ErrorIs(t, err, platformauthz.ErrDecisionUnavailable)
	require.Empty(t, revision)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT DISTINCT res.resource_key,act.action_key FROM platform_principal_permissions`).
		WillReturnRows(sqlmock.NewRows([]string{"resource_key", "action_key"}))
	mock.ExpectQuery(`SELECT DISTINCT res.resource_key,act.action_key FROM permissions`).
		WillReturnRows(sqlmock.NewRows([]string{"resource_key", "action_key"}))
	mock.ExpectCommit().WillReturnError(errors.New("commit unavailable"))
	_, revision, err = a.Snapshot(t.Context(), principal)
	require.ErrorIs(t, err, platformauthz.ErrDecisionUnavailable)
	require.Empty(t, revision)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerSnapshotRejectsOversizedGrantSets(t *testing.T) {
	for _, scope := range []string{"platform", "tenant"} {
		t.Run(scope, func(t *testing.T) {
			a, mock := testAuthorizer(t)
			principal := platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}
			rows := sqlmock.NewRows([]string{"resource_key", "action_key"})
			for index := range maxEffectiveGrantActions + 1 {
				rows.AddRow(fmt.Sprintf("resource.%d", index), "read")
			}
			mock.ExpectBegin()
			if scope == "platform" {
				mock.ExpectQuery(`FROM platform_principal_permissions.*LIMIT 10001`).WillReturnRows(rows)
			} else {
				principal.TenantID = "tenant-1"
				principal.MembershipID = "member-1"
				mock.ExpectQuery(`FROM platform_principal_permissions.*LIMIT 10001`).
					WillReturnRows(sqlmock.NewRows([]string{"resource_key", "action_key"}))
				mock.ExpectQuery(`FROM permissions.*LIMIT 10001`).WillReturnRows(rows)
			}
			mock.ExpectRollback()
			_, revision, err := a.Snapshot(t.Context(), principal)
			require.ErrorIs(t, err, platformauthz.ErrDecisionUnavailable)
			require.Empty(t, revision)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
