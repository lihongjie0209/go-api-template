package authorization

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type capabilityAuthorizerFunc func(context.Context, platformprincipal.Principal, platformauthz.Requirement) error

func (f capabilityAuthorizerFunc) Authorize(ctx context.Context, principal platformprincipal.Principal, requirement platformauthz.Requirement) error {
	return f(ctx, principal, requirement)
}

func TestCapabilityServiceEvaluatesAnyRegisteredTargetOperation(t *testing.T) {
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	endpoints, err := accesscontrol.NewEndpointRegistry(resources, []accesscontrol.Endpoint{
		{Transport: accesscontrol.TransportHTTP, Operation: "POST /api/v1/users/update", Authentication: accesscontrol.AuthenticationJWT, Resource: "identity.user", Action: "update", DataPermission: accesscontrol.DataPermissionNone, DataPermissionReason: "platform resource"},
		{Transport: accesscontrol.TransportHTTP, Operation: "POST /api/v1/users/status/update", Authentication: accesscontrol.AuthenticationJWT, Resource: "identity.user", Action: "update", DataPermission: accesscontrol.DataPermissionNone, DataPermissionReason: "platform resource"},
	})
	require.NoError(t, err)
	seen := []string{}
	service := NewCapabilityService(nil, capabilityAuthorizerFunc(func(ctx context.Context, _ platformprincipal.Principal, _ platformauthz.Requirement) error {
		endpoint, ok := accesscontrol.EndpointFromContext(ctx)
		require.True(t, ok)
		seen = append(seen, endpoint.Operation)
		if endpoint.Operation == "POST /api/v1/users/status/update" {
			return nil
		}
		return platformauthz.ErrDenied
	}), nil, resources)
	service.SetEndpointRegistry(endpoints)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})

	result, err := service.Evaluate(ctx, []CapabilityRequest{{Key: "user.update", Resource: "identity.user", Action: "update"}})
	require.NoError(t, err)
	require.Equal(t, []CapabilityDecision{{Key: "user.update", Allowed: true}}, result)
	require.Equal(t, []string{"POST /api/v1/users/status/update"}, seen)
}

func TestCapabilityServiceEvaluatesRowsInOneTenantBoundedQuery(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	schema, err := datapermission.NewSchema("tenant.member", map[string]datapermission.Field{
		"id": {Column: "tm.id", Type: datapermission.ValueTypeText}, "owner_id": {Column: "tm.user_id", Type: datapermission.ValueTypeText},
		"status": {Column: "tm.status", Type: datapermission.ValueTypeText}, "created_by": {Column: "tm.created_by", Type: datapermission.ValueTypeText},
	})
	require.NoError(t, err)
	schemas, err := datapermission.NewSchemaRegistry(schema)
	require.NoError(t, err)
	engine, err := datapermission.NewEngine(schemas, resources, []datapermission.Policy{{
		APIVersion: datapermission.PolicyAPIVersion, Kind: datapermission.PolicyKind,
		Metadata: datapermission.PolicyMetadata{Code: "member-update-own", Name: "Member update own"},
		Scope:    datapermission.PolicyBoundary{Type: datapermission.PolicyScopeTenant, TenantID: "tenant-1"},
		Spec:     datapermission.PolicySpec{Resource: "tenant.member", Actions: []string{"update"}, Condition: "resource.owner_id == subject.id", Effect: datapermission.EffectAllow},
	}})
	require.NoError(t, err)
	dataScopes := datapermission.NewService(db, engine)
	service := NewCapabilityService(db, capabilityAuthorizerFunc(func(context.Context, platformprincipal.Principal, platformauthz.Requirement) error { return nil }), dataScopes, resources)
	endpoints, err := accesscontrol.NewEndpointRegistry(resources, []accesscontrol.Endpoint{{Transport: accesscontrol.TransportHTTP, Operation: "POST /api/v1/tenant-members/status/update", Authentication: accesscontrol.AuthenticationJWT, Resource: "tenant.member", Action: "update", DataPermission: accesscontrol.DataPermissionRequired}})
	require.NoError(t, err)
	service.SetEndpointRegistry(endpoints)
	mock.ExpectQuery(`SELECT id,user_id AS owner_id,status,created_by FROM tenant_memberships WHERE tenant_id=\? AND id IN \(\?, \?\) AND deleted_at IS NULL`).
		WithArgs("tenant-1", "member-1", "missing-member").
		WillReturnRows(sqlmock.NewRows([]string{"id", "owner_id", "status", "created_by"}).AddRow("member-1", "user-1", "active", "admin-1"))
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeServiceAccount, TenantID: "tenant-1"})

	result, err := service.EvaluateRows(ctx, "tenant.member", []string{"update"}, []string{"member-1", "missing-member"})
	require.NoError(t, err)
	require.Equal(t, RowCapabilityResult{Resource: "tenant.member", Items: []RowCapabilityDecision{
		{ResourceID: "member-1", Actions: map[string]bool{"update": true}},
		{ResourceID: "missing-member", Actions: map[string]bool{"update": false}},
	}}, result)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCapabilityServiceFailsClosedWhenDecisionIsUnavailable(t *testing.T) {
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	endpoints, err := accesscontrol.NewEndpointRegistry(resources, []accesscontrol.Endpoint{{Transport: accesscontrol.TransportHTTP, Operation: "POST /api/v1/users/update", Authentication: accesscontrol.AuthenticationJWT, Resource: "identity.user", Action: "update", DataPermission: accesscontrol.DataPermissionNone, DataPermissionReason: "platform resource"}})
	require.NoError(t, err)
	service := NewCapabilityService(nil, capabilityAuthorizerFunc(func(context.Context, platformprincipal.Principal, platformauthz.Requirement) error {
		return platformauthz.ErrDecisionUnavailable
	}), nil, resources)
	service.SetEndpointRegistry(endpoints)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})

	_, err = service.Evaluate(ctx, []CapabilityRequest{{Key: "user.update", Resource: "identity.user", Action: "update"}})
	require.True(t, errors.Is(err, ErrCapabilityUnavailable))
}

func TestCapabilityServiceRejectsDuplicateRowIDs(t *testing.T) {
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	service := NewCapabilityService(nil, nil, nil, resources)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})

	_, err = service.EvaluateRows(ctx, "tenant.member", []string{"update"}, []string{"member-1", "member-1"})
	require.ErrorIs(t, err, ErrCapabilityInvalid)
}
