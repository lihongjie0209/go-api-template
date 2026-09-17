package authorization

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type capabilityAuthorizerFunc func(context.Context, platformprincipal.Principal, platformauthz.Requirement) error

type staticRowCapabilityProvider struct {
	resource string
	rows     map[string]datapermission.ResourceAttributes
}

func (p staticRowCapabilityProvider) Resource() string { return p.resource }
func (p staticRowCapabilityProvider) Load(context.Context, string, []string) (map[string]datapermission.ResourceAttributes, error) {
	return p.rows, nil
}

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
	service := newCapabilityService(capabilityAuthorizerFunc(func(ctx context.Context, _ platformprincipal.Principal, _ platformauthz.Requirement) error {
		endpoint, ok := accesscontrol.EndpointFromContext(ctx)
		require.True(t, ok)
		seen = append(seen, endpoint.Operation)
		if endpoint.Operation == "POST /api/v1/users/status/update" {
			return nil
		}
		return platformauthz.ErrDenied
	}), nil, resources, nil, nil, func() time.Time { return time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC) })
	service.SetEndpointRegistry(endpoints)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})

	result, err := service.Evaluate(ctx, []CapabilityRequest{{Key: "user.update", Resource: "identity.user", Action: "update"}})
	require.NoError(t, err)
	require.Equal(t, []CapabilityDecision{{Key: "user.update", Allowed: true}}, result.Items)
	require.Equal(t, time.Date(2026, 9, 17, 18, 0, 30, 0, operationLocation), result.ExpiresAt)
	require.Equal(t, []string{"POST /api/v1/users/status/update"}, seen)
}

func TestCapabilityServiceEvaluatesRowsInOneTenantBoundedQuery(t *testing.T) {
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
	dataScopes := datapermission.NewService(nil, engine)
	providers, err := NewRowCapabilityRegistry([]RowCapabilityProvider{staticRowCapabilityProvider{resource: "tenant.member", rows: map[string]datapermission.ResourceAttributes{
		"member-1": {"id": "member-1", "owner_id": "user-1", "status": "active", "created_by": "admin-1"},
	}}}, resources, schemas)
	require.NoError(t, err)
	service := newCapabilityService(capabilityAuthorizerFunc(func(context.Context, platformprincipal.Principal, platformauthz.Requirement) error { return nil }), dataScopes, resources, providers, nil, func() time.Time { return time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC) })
	endpoints, err := accesscontrol.NewEndpointRegistry(resources, []accesscontrol.Endpoint{{Transport: accesscontrol.TransportHTTP, Operation: "POST /api/v1/tenant-members/status/update", Authentication: accesscontrol.AuthenticationJWT, Resource: "tenant.member", Action: "update", DataPermission: accesscontrol.DataPermissionRequired}})
	require.NoError(t, err)
	service.SetEndpointRegistry(endpoints)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeServiceAccount, TenantID: "tenant-1"})

	result, err := service.EvaluateRows(ctx, "tenant.member", []string{"update"}, []string{"member-1", "missing-member"})
	require.NoError(t, err)
	require.Equal(t, RowCapabilityResult{ExpiresAt: time.Date(2026, 9, 17, 18, 0, 30, 0, operationLocation), Resource: "tenant.member", Items: []RowCapabilityDecision{
		{ResourceID: "member-1", Actions: map[string]bool{"update": true}},
		{ResourceID: "missing-member", Actions: map[string]bool{"update": false}},
	}}, result)
}

func TestCapabilityServiceRejectsProviderRowsOutsideRequestedSet(t *testing.T) {
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	schema, err := datapermission.NewSchema("tenant.member", map[string]datapermission.Field{
		"id": {Column: "tm.id", Type: datapermission.ValueTypeText},
	})
	require.NoError(t, err)
	schemas, err := datapermission.NewSchemaRegistry(schema)
	require.NoError(t, err)
	engine, err := datapermission.NewEngine(schemas, resources, nil)
	require.NoError(t, err)
	providers, err := NewRowCapabilityRegistry([]RowCapabilityProvider{staticRowCapabilityProvider{
		resource: "tenant.member",
		rows: map[string]datapermission.ResourceAttributes{
			"other-tenant-row": {"id": "other-tenant-row"},
		},
	}}, resources, schemas)
	require.NoError(t, err)
	service := newCapabilityService(
		capabilityAuthorizerFunc(func(context.Context, platformprincipal.Principal, platformauthz.Requirement) error { return nil }),
		datapermission.NewService(nil, engine), resources, providers, nil, time.Now,
	)
	endpoints, err := accesscontrol.NewEndpointRegistry(resources, []accesscontrol.Endpoint{{
		Transport: accesscontrol.TransportHTTP, Operation: "POST /api/v1/tenant-members/get", Authentication: accesscontrol.AuthenticationJWT,
		Resource: "tenant.member", Action: "read", DataPermission: accesscontrol.DataPermissionRequired,
	}})
	require.NoError(t, err)
	service.SetEndpointRegistry(endpoints)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})

	_, err = service.EvaluateRows(ctx, "tenant.member", []string{"read"}, []string{"member-1"})
	require.ErrorIs(t, err, ErrCapabilityUnavailable)
}

func TestCapabilityServiceFailsClosedWhenDecisionIsUnavailable(t *testing.T) {
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	endpoints, err := accesscontrol.NewEndpointRegistry(resources, []accesscontrol.Endpoint{{Transport: accesscontrol.TransportHTTP, Operation: "POST /api/v1/users/update", Authentication: accesscontrol.AuthenticationJWT, Resource: "identity.user", Action: "update", DataPermission: accesscontrol.DataPermissionNone, DataPermissionReason: "platform resource"}})
	require.NoError(t, err)
	service := newCapabilityService(capabilityAuthorizerFunc(func(context.Context, platformprincipal.Principal, platformauthz.Requirement) error {
		return platformauthz.ErrDecisionUnavailable
	}), nil, resources, nil, nil, time.Now)
	service.SetEndpointRegistry(endpoints)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})

	_, err = service.Evaluate(ctx, []CapabilityRequest{{Key: "user.update", Resource: "identity.user", Action: "update"}})
	require.True(t, errors.Is(err, ErrCapabilityUnavailable))
}

func TestCapabilityServiceRejectsDuplicateRowIDs(t *testing.T) {
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	service := newCapabilityService(nil, nil, resources, nil, nil, time.Now)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})

	_, err = service.EvaluateRows(ctx, "tenant.member", []string{"update"}, []string{"member-1", "member-1"})
	require.ErrorIs(t, err, ErrCapabilityInvalid)
}
