package authorization

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/environment"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestAuthorizerUsesLocalPBACEngine(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	engine := authorizationTestEngine(t, pbac.SubjectMatcher{Types: []string{"user"}})
	authorizer := &Authorizer{db: sqlx.NewDb(db, "sqlmock"), engine: engine, registry: authorizationTestRegistry(t)}

	err = authorizer.Authorize(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}, platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopePlatform})
	require.NoError(t, err)
	err = authorizer.Authorize(t.Context(), platformprincipal.Principal{ID: "service-1", Type: platformprincipal.TypeServiceAccount}, platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopePlatform})
	require.ErrorIs(t, err, platformauthz.ErrDenied)
}

func TestAuthorizerBuildsTrustedOperationConditionContext(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	registry := authorizationTestRegistry(t)
	engine, err := pbac.NewEngine(registry, []pbac.Policy{{
		APIVersion: pbac.APIVersionV1, Kind: pbac.KindPolicy,
		Metadata: pbac.PolicyMetadata{Code: "working-hours", Name: "Working hours"}, Scope: pbac.PolicyScope{Type: pbac.PolicyScopeGlobal},
		Spec: pbac.PolicySpec{Subject: pbac.SubjectMatcher{Types: []string{"user"}}, Resource: pbac.ResourceMatcher{Type: "identity.user"}, Actions: []string{"read"},
			When: `environment.profile == "production" && environment.weekday == 1 && environment.local_hour == 9 && request.transport == "http" && request.operation == "POST /users/get" && authentication.scheme == "bearer"`, Effect: pbac.EffectAllow},
	}})
	require.NoError(t, err)
	authorizer := newAuthorizer(sqlx.NewDb(db, "sqlmock"), engine, registry, func() time.Time {
		return time.Date(2026, time.September, 14, 9, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	})
	ctx := environment.WithContext(t.Context(), "production")
	ctx = accesscontrol.WithEndpoint(ctx, accesscontrol.Endpoint{Transport: accesscontrol.TransportHTTP, Operation: "POST /users/get"})
	ctx = accesscontrol.WithCredentialScheme(ctx, accesscontrol.CredentialSchemeBearer)

	err = authorizer.Authorize(ctx, platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}, platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopePlatform})
	require.NoError(t, err)
}

func TestAuthorizerResolvesTrustedTenantRoles(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	engine := authorizationTestEngine(t, pbac.SubjectMatcher{Roles: pbac.RolesMatcher{AnyOf: []string{"department_manager"}}})
	authorizer := &Authorizer{db: sqlx.NewDb(db, "sqlmock"), engine: engine, registry: authorizationTestRegistry(t)}
	mock.ExpectQuery(`SELECT DISTINCT r.code`).WithArgs("user-1", "tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"code"}).AddRow("department_manager"))

	err = authorizer.Authorize(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"}, platformauthz.Requirement{Resource: "tenant.member", Action: "read", Scope: platformauthz.ScopeTenant})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerMapsEvaluationFailureToUnavailable(t *testing.T) {
	authorizer := &Authorizer{}
	err := authorizer.Authorize(t.Context(), platformprincipal.Principal{ID: "user-1"}, platformauthz.Requirement{Resource: "identity.user", Action: "read"})
	require.True(t, errors.Is(err, platformauthz.ErrDecisionUnavailable))
}

func TestAuthorizerAllowsManagedTenantRoleGrantWhenNoPolicyMatches(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	registry := authorizationTestRegistry(t)
	engine, err := pbac.NewEngine(registry, nil)
	require.NoError(t, err)
	authorizer := &Authorizer{db: sqlx.NewDb(db, "sqlmock"), engine: engine, registry: registry}
	mock.ExpectQuery(`SELECT DISTINCT r.code`).WithArgs("user-1", "tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"code"}).AddRow("auditor"))
	mock.ExpectQuery(`SELECT count\(\*\).*FROM permissions p`).WithArgs("tenant-1", "member-1", "user-1", "tenant.member", "read").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	err = authorizer.Authorize(t.Context(), tenantPrincipal(), platformauthz.Requirement{Resource: "tenant.member", Action: "read", Scope: platformauthz.ScopeTenant})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerExplicitDenyOverridesManagedTenantGrant(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	registry := authorizationTestRegistry(t)
	engine := authorizationEngineWithEffect(t, registry, pbac.EffectDeny)
	authorizer := &Authorizer{db: sqlx.NewDb(db, "sqlmock"), engine: engine, registry: registry}
	mock.ExpectQuery(`SELECT DISTINCT r.code`).WithArgs("user-1", "tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"code"}))

	err = authorizer.Authorize(t.Context(), tenantPrincipal(), platformauthz.Requirement{Resource: "tenant.member", Action: "read", Scope: platformauthz.ScopeTenant})
	require.ErrorIs(t, err, platformauthz.ErrDenied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerManagedGrantLookupFailureIsUnavailable(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	registry := authorizationTestRegistry(t)
	engine, err := pbac.NewEngine(registry, nil)
	require.NoError(t, err)
	authorizer := &Authorizer{db: sqlx.NewDb(db, "sqlmock"), engine: engine, registry: registry}
	mock.ExpectQuery(`SELECT DISTINCT r.code`).WithArgs("user-1", "tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"code"}))
	mock.ExpectQuery(`SELECT count\(\*\).*FROM permissions p`).WillReturnError(context.DeadlineExceeded)

	err = authorizer.Authorize(t.Context(), tenantPrincipal(), platformauthz.Requirement{Resource: "tenant.member", Action: "read", Scope: platformauthz.ScopeTenant})
	require.ErrorIs(t, err, platformauthz.ErrDecisionUnavailable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerRejectsRequirementScopeDrift(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	registry := authorizationTestRegistry(t)
	engine, err := pbac.NewEngine(registry, nil)
	require.NoError(t, err)
	authorizer := &Authorizer{db: sqlx.NewDb(db, "sqlmock"), engine: engine, registry: registry}

	err = authorizer.Authorize(t.Context(), tenantPrincipal(), platformauthz.Requirement{Resource: "identity.user", Action: "read", Scope: platformauthz.ScopeTenant})
	require.ErrorIs(t, err, platformauthz.ErrDecisionUnavailable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizerDoesNotTreatServiceAccountAsTenantMember(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	registry := authorizationTestRegistry(t)
	engine, err := pbac.NewEngine(registry, nil)
	require.NoError(t, err)
	authorizer := &Authorizer{db: sqlx.NewDb(db, "sqlmock"), engine: engine, registry: registry}
	principal := platformprincipal.Principal{ID: "service-1", Type: platformprincipal.TypeServiceAccount, TenantID: "tenant-1", MembershipID: "member-1"}

	err = authorizer.Authorize(t.Context(), principal, platformauthz.Requirement{Resource: "tenant.member", Action: "read", Scope: platformauthz.ScopeTenant})
	require.ErrorIs(t, err, platformauthz.ErrDenied)
	require.NoError(t, mock.ExpectationsWereMet(), "service accounts must not query or inherit human membership roles")
}

func TestAuthorizerAllowsPrincipalScopeWithoutTenantContext(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	registry := authorizationTestRegistry(t)
	authenticated := true
	engine, err := pbac.NewEngine(registry, []pbac.Policy{{
		APIVersion: pbac.APIVersionV1, Kind: pbac.KindPolicy,
		Metadata: pbac.PolicyMetadata{Code: "profile-read", Name: "Profile read"},
		Scope:    pbac.PolicyScope{Type: pbac.PolicyScopeGlobal},
		Spec: pbac.PolicySpec{
			Subject: pbac.SubjectMatcher{Authenticated: &authenticated}, Resource: pbac.ResourceMatcher{Type: "identity.profile"},
			Actions: []string{"read"}, Effect: pbac.EffectAllow,
		},
	}})
	require.NoError(t, err)
	authorizer := &Authorizer{db: sqlx.NewDb(db, "sqlmock"), engine: engine, registry: registry}

	err = authorizer.Authorize(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}, platformauthz.Requirement{Resource: "identity.profile", Action: "read", Scope: platformauthz.ScopePrincipal})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func tenantPrincipal() platformprincipal.Principal {
	return platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"}
}

func authorizationEngineWithEffect(t *testing.T, registry *pbac.Registry, effect pbac.Effect) *pbac.Engine {
	t.Helper()
	authenticated := true
	engine, err := pbac.NewEngine(registry, []pbac.Policy{{
		APIVersion: pbac.APIVersionV1, Kind: pbac.KindPolicy,
		Metadata: pbac.PolicyMetadata{Code: "explicit-policy", Name: "Explicit policy"},
		Scope:    pbac.PolicyScope{Type: pbac.PolicyScopeTenant, TenantID: "tenant-1"},
		Spec: pbac.PolicySpec{
			Subject:  pbac.SubjectMatcher{Authenticated: &authenticated},
			Resource: pbac.ResourceMatcher{Type: "tenant.member"},
			Actions:  []string{"read"}, Effect: effect,
		},
	}})
	require.NoError(t, err)
	return engine
}

func authorizationTestEngine(t *testing.T, subject pbac.SubjectMatcher) *pbac.Engine {
	t.Helper()
	registry := authorizationTestRegistry(t)
	authenticated := true
	resource := "identity.user"
	scope := pbac.PolicyScope{Type: pbac.PolicyScopeGlobal}
	if len(subject.Roles.AnyOf) > 0 {
		resource = "tenant.member"
		scope = pbac.PolicyScope{Type: pbac.PolicyScopeTenant, TenantID: "tenant-1"}
	}
	engine, err := pbac.NewEngine(registry, []pbac.Policy{{
		APIVersion: pbac.APIVersionV1, Kind: pbac.KindPolicy,
		Metadata: pbac.PolicyMetadata{Code: "test-policy", Name: "Test policy"}, Scope: scope,
		Spec: pbac.PolicySpec{Subject: func() pbac.SubjectMatcher { subject.Authenticated = &authenticated; return subject }(), Resource: pbac.ResourceMatcher{Type: resource}, Actions: []string{"read"}, Effect: pbac.EffectAllow},
	}})
	require.NoError(t, err)
	return engine
}

func authorizationTestRegistry(t *testing.T) *pbac.Registry {
	t.Helper()
	registry, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	return registry
}
