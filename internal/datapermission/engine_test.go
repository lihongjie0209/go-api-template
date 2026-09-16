package datapermission

import (
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/stretchr/testify/require"
)

func TestSchemaRegistryRejectsRequiredEndpointWithoutSchema(t *testing.T) {
	t.Parallel()
	member, err := NewSchema("tenant.member", map[string]Field{"id": {Column: "tm.id", Type: ValueTypeText}})
	require.NoError(t, err)
	registry, err := NewSchemaRegistry(member)
	require.NoError(t, err)

	require.NoError(t, registry.ValidateEndpoint(accesscontrol.Endpoint{Operation: "POST /users/get", Resource: "identity.user", DataPermission: accesscontrol.DataPermissionNone}))
	require.ErrorIs(t, registry.ValidateEndpoint(accesscontrol.Endpoint{Operation: "POST /users/get", Resource: "identity.user", DataPermission: accesscontrol.DataPermissionRequired}), ErrInvalidSchema)
	require.NoError(t, registry.ValidateEndpoint(accesscontrol.Endpoint{Operation: "POST /members/get", Resource: "tenant.member", DataPermission: accesscontrol.DataPermissionRequired}))
}

func TestEngineMatchesAndCombinesPublishedPolicies(t *testing.T) {
	schema, err := NewSchema("tenant.member", map[string]Field{
		"department_id": {Column: "tm.department_id", Type: ValueTypeText},
		"owner_id":      {Column: "tm.user_id", Type: ValueTypeText},
	})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	authenticated := true
	base := Policy{
		APIVersion: PolicyAPIVersion, Kind: PolicyKind,
		Scope: PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec:  PolicySpec{Subject: pbac.SubjectMatcher{Authenticated: &authenticated, Roles: pbac.RolesMatcher{AnyOf: []string{"manager"}}}, Resource: "tenant.member", Actions: []string{"read"}, Effect: EffectAllow},
	}
	allow := base
	allow.Metadata = PolicyMetadata{Code: "manager-departments", Name: "Manager departments"}
	allow.Spec.Condition = "resource.department_id in subject.department_ids"
	deny := base
	deny.Metadata = PolicyMetadata{Code: "exclude-owner", Name: "Exclude owner"}
	deny.Spec.Effect = EffectDeny
	deny.Spec.Condition = `resource.owner_id == "blocked-user"`
	engine, err := NewEngine(schemas, resources, []Policy{allow, deny})
	require.NoError(t, err)

	sql, err := engine.CompileSQL(t.Context(), "tenant.member", "read", pbac.Subject{ID: "u1", Type: "user", Authenticated: true, TenantID: "tenant-1", Roles: []string{"manager"}}, SubjectAttributes{"department_ids": []string{"d1", "d2"}})
	require.NoError(t, err)
	require.Equal(t, "((tm.department_id IN (?, ?)) AND (NOT (tm.user_id = ?)))", sql.Clause)
	require.Equal(t, []any{"d1", "d2", "blocked-user"}, sql.Args)
}

func TestEngineNoMatchingAllowIsFalse(t *testing.T) {
	schema, err := NewSchema("tenant.member", map[string]Field{"owner_id": {Column: "tm.user_id", Type: ValueTypeText}})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	engine, err := NewEngine(schemas, resources, nil)
	require.NoError(t, err)
	sql, err := engine.CompileSQL(t.Context(), "tenant.member", "list", pbac.Subject{Authenticated: true, TenantID: "tenant-1"}, nil)
	require.NoError(t, err)
	require.Equal(t, "(1 = 0)", sql.Clause)
}

func TestEngineEvaluatesProposedObjectWithDenyOverrides(t *testing.T) {
	schema, err := NewSchema("tenant.member", map[string]Field{"owner_id": {Column: "tm.user_id", Type: ValueTypeText}})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	authenticated := true
	base := Policy{
		APIVersion: PolicyAPIVersion, Kind: PolicyKind,
		Scope: PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec:  PolicySpec{Subject: pbac.SubjectMatcher{Authenticated: &authenticated}, Resource: "tenant.member", Actions: []string{"add"}, Effect: EffectAllow},
	}
	allow := base
	allow.Metadata = PolicyMetadata{Code: "member-create-own", Name: "Create own member"}
	allow.Spec.Condition = "resource.owner_id == subject.id"
	deny := base
	deny.Metadata = PolicyMetadata{Code: "member-create-blocked", Name: "Block user"}
	deny.Spec.Effect = EffectDeny
	deny.Spec.Condition = `resource.owner_id == "blocked"`
	engine, err := NewEngine(schemas, resources, []Policy{allow, deny})
	require.NoError(t, err)
	subject := pbac.Subject{ID: "user-1", Type: "user", Authenticated: true, TenantID: "tenant-1"}
	attributes := SubjectAttributes{"id": "user-1"}

	allowed, err := engine.EvaluateObject(t.Context(), "tenant.member", "add", subject, attributes, ResourceAttributes{"owner_id": "user-1"})
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = engine.EvaluateObject(t.Context(), "tenant.member", "add", subject, attributes, ResourceAttributes{"owner_id": "blocked"})
	require.NoError(t, err)
	require.False(t, allowed)
}
