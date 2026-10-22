package datapermission

import (
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
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
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	authenticated := true
	base := Policy{
		APIVersion: PolicyAPIVersion, Kind: PolicyKind,
		Scope: PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec:  PolicySpec{Subject: resourceaction.SubjectMatcher{Authenticated: &authenticated, Roles: resourceaction.RolesMatcher{AnyOf: []string{"manager"}}}, Resource: "tenant.member", Actions: []string{"read"}, Effect: EffectAllow},
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

	sql, err := engine.CompileSQL(t.Context(), "tenant.member", "read", resourceaction.Subject{ID: "u1", Type: "user", Authenticated: true, TenantID: "tenant-1", Roles: []string{"manager"}}, SubjectAttributes{"department_ids": []string{"d1", "d2"}})
	require.NoError(t, err)
	require.Equal(t, "((tm.department_id IN (?, ?)) AND (NOT (tm.user_id = ?)))", sql.Clause)
	require.Equal(t, []any{"d1", "d2", "blocked-user"}, sql.Args)
}

func TestEngineNoMatchingAllowIsFalse(t *testing.T) {
	schema, err := NewSchema("tenant.member", map[string]Field{"owner_id": {Column: "tm.user_id", Type: ValueTypeText}})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	engine, err := NewEngine(schemas, resources, nil)
	require.NoError(t, err)
	sql, err := engine.CompileSQL(t.Context(), "tenant.member", "list", resourceaction.Subject{Authenticated: true, TenantID: "tenant-1"}, nil)
	require.NoError(t, err)
	require.Equal(t, "(1 = 0)", sql.Clause)
}

func TestEnginePinnedSnapshotKeepsRevisionAndDecisionsTogether(t *testing.T) {
	schema, err := NewSchema("tenant.member", map[string]Field{"owner_id": {Column: "tm.user_id", Type: ValueTypeText}})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	engine, err := NewRuntimeEngine(schemas, resources)
	require.NoError(t, err)
	pinnedCtx, before, err := engine.Pin(t.Context())
	require.NoError(t, err)
	subject := resourceaction.Subject{ID: "user-1", Type: "user", Authenticated: true, TenantID: "tenant-1"}
	attributes := SubjectAttributes{"id": "user-1"}
	row := ResourceAttributes{"owner_id": "user-1"}
	firstAllowed, err := engine.EvaluateCurrent(pinnedCtx, "tenant.member", "read", subject, attributes, row)
	require.NoError(t, err)
	require.False(t, firstAllowed)
	policy := Policy{
		APIVersion: PolicyAPIVersion, Kind: PolicyKind,
		Metadata: PolicyMetadata{Code: "member-owner", Name: "Member owner"},
		Scope:    PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec:     PolicySpec{Resource: "tenant.member", Actions: []string{"read", "update"}, Condition: "resource.owner_id == subject.id", Effect: EffectAllow},
	}
	require.NoError(t, engine.Replace([]Policy{policy}))
	currentCtx, after, err := engine.Pin(t.Context())
	require.NoError(t, err)
	require.NotEqual(t, before, after)
	for _, action := range []string{"read", "update"} {
		oldAllowed, oldErr := engine.EvaluateCurrent(pinnedCtx, "tenant.member", action, subject, attributes, row)
		require.NoError(t, oldErr)
		require.False(t, oldAllowed)
		newAllowed, newErr := engine.EvaluateCurrent(currentCtx, "tenant.member", action, subject, attributes, row)
		require.NoError(t, newErr)
		require.True(t, newAllowed)
	}
	oldSQL, err := engine.CompileSQL(pinnedCtx, "tenant.member", "read", subject, attributes)
	require.NoError(t, err)
	require.Equal(t, "(1 = 0)", oldSQL.Clause)
	newSQL, err := engine.CompileSQL(currentCtx, "tenant.member", "read", subject, attributes)
	require.NoError(t, err)
	require.Equal(t, "(tm.user_id = ?)", newSQL.Clause)
	second := policy
	second.Metadata = PolicyMetadata{Code: "member-owner-update", Name: "Member owner update"}
	second.Spec.Actions = []string{"update"}
	require.NoError(t, engine.Replace([]Policy{policy, second}))
	_, forwardRevision, err := engine.Pin(t.Context())
	require.NoError(t, err)
	require.NoError(t, engine.Replace([]Policy{second, policy}))
	_, reversedRevision, err := engine.Pin(t.Context())
	require.NoError(t, err)
	require.Equal(t, forwardRevision, reversedRevision)
}

func TestEngineInvalidationFailsClosedUntilCompleteReplacement(t *testing.T) {
	t.Parallel()
	schema, err := NewSchema("tenant.member", map[string]Field{"owner_id": {Column: "tm.user_id", Type: ValueTypeText}})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	engine, err := NewRuntimeEngine(schemas, resources)
	require.NoError(t, err)
	_, _, err = engine.Pin(t.Context())
	require.NoError(t, err)

	engine.Invalidate()
	_, _, err = engine.Pin(t.Context())
	require.ErrorIs(t, err, ErrSnapshotUnavailable)
	_, err = engine.CompileSQL(t.Context(), "tenant.member", "read", resourceaction.Subject{Authenticated: true, TenantID: "tenant-1"}, nil)
	require.ErrorIs(t, err, ErrSnapshotUnavailable)

	require.NoError(t, engine.Replace(nil))
	_, _, err = engine.Pin(t.Context())
	require.NoError(t, err)
}

func TestEngineReplaceDoesNotRetainCallerPolicyMemory(t *testing.T) {
	schema, err := NewSchema("tenant.member", map[string]Field{"owner_id": {Column: "tm.user_id", Type: ValueTypeText}})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	authenticated := true
	policy := Policy{
		APIVersion: PolicyAPIVersion, Kind: PolicyKind,
		Metadata: PolicyMetadata{Code: "member-own", Name: "Member own"},
		Scope:    PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec: PolicySpec{Subject: resourceaction.SubjectMatcher{Authenticated: &authenticated, Roles: resourceaction.RolesMatcher{AnyOf: []string{"manager"}}},
			Resource: "tenant.member", Actions: []string{"read"}, Condition: "resource.owner_id == subject.id", Effect: EffectAllow},
	}
	engine, err := NewEngine(schemas, resources, []Policy{policy})
	require.NoError(t, err)
	_, revision, err := engine.Pin(t.Context())
	require.NoError(t, err)
	authenticated = false
	policy.Spec.Subject.Roles.AnyOf[0] = "other"
	policy.Spec.Actions[0] = "update"

	subject := resourceaction.Subject{ID: "user-1", Type: "user", Authenticated: true, TenantID: "tenant-1", Roles: []string{"manager"}}
	allowed, err := engine.EvaluateCurrent(t.Context(), "tenant.member", "read", subject, SubjectAttributes{"id": "user-1"}, ResourceAttributes{"owner_id": "user-1"})
	require.NoError(t, err)
	require.True(t, allowed)
	_, after, err := engine.Pin(t.Context())
	require.NoError(t, err)
	require.Equal(t, revision, after)
}

func TestEngineEvaluatesProposedObjectWithDenyOverrides(t *testing.T) {
	schema, err := NewSchema("tenant.member", map[string]Field{"owner_id": {Column: "tm.user_id", Type: ValueTypeText}})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	authenticated := true
	base := Policy{
		APIVersion: PolicyAPIVersion, Kind: PolicyKind,
		Scope: PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec:  PolicySpec{Subject: resourceaction.SubjectMatcher{Authenticated: &authenticated}, Resource: "tenant.member", Actions: []string{"add"}, Effect: EffectAllow},
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
	subject := resourceaction.Subject{ID: "user-1", Type: "user", Authenticated: true, TenantID: "tenant-1"}
	attributes := SubjectAttributes{"id": "user-1"}

	allowed, err := engine.EvaluateObject(t.Context(), "tenant.member", "add", subject, attributes, ResourceAttributes{"owner_id": "user-1"})
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = engine.EvaluateObject(t.Context(), "tenant.member", "add", subject, attributes, ResourceAttributes{"owner_id": "blocked"})
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestEngineEvaluatesCurrentAndProposedStateTogether(t *testing.T) {
	schema, err := NewSchema("tenant.member", map[string]Field{
		"owner_id": {Column: "tm.user_id", Type: ValueTypeText},
		"status":   {Column: "tm.status", Type: ValueTypeText},
	})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	policy := Policy{
		APIVersion: PolicyAPIVersion, Kind: PolicyKind,
		Metadata: PolicyMetadata{Code: "member-activate-own", Name: "Activate own member"},
		Scope:    PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec: PolicySpec{
			Resource: "tenant.member", Actions: []string{"update"}, Effect: EffectAllow,
			Condition: `resource.owner_id == subject.id`, ProposedCondition: `proposed.status == "active"`,
		},
	}
	engine, err := NewEngine(schemas, resources, []Policy{policy})
	require.NoError(t, err)
	subject := resourceaction.Subject{ID: "user-1", TenantID: "tenant-1"}
	attributes := SubjectAttributes{"id": "user-1"}

	allowed, err := engine.EvaluateTransition(t.Context(), "tenant.member", "update", subject, attributes,
		ResourceAttributes{"owner_id": "user-1", "status": "disabled"}, ResourceAttributes{"owner_id": "user-1", "status": "active"})
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = engine.EvaluateTransition(t.Context(), "tenant.member", "update", subject, attributes,
		ResourceAttributes{"owner_id": "user-1", "status": "disabled"}, ResourceAttributes{"owner_id": "user-1", "status": "disabled"})
	require.NoError(t, err)
	require.False(t, allowed)
	allowed, err = engine.EvaluateTransition(t.Context(), "tenant.member", "update", subject, attributes,
		ResourceAttributes{"owner_id": "other", "status": "disabled"}, ResourceAttributes{"owner_id": "other", "status": "active"})
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestPolicyRejectsProposedConditionForReadAction(t *testing.T) {
	schema, err := NewSchema("tenant.member", map[string]Field{"status": {Column: "tm.status", Type: ValueTypeText}})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	policy := Policy{
		APIVersion: PolicyAPIVersion, Kind: PolicyKind,
		Metadata: PolicyMetadata{Code: "invalid-read-target", Name: "Invalid read target"},
		Scope:    PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec: PolicySpec{Resource: "tenant.member", Actions: []string{"read"}, Effect: EffectAllow,
			ProposedCondition: `proposed.status == "active"`},
	}
	_, err = policy.Compile(schemas, resources)
	require.ErrorIs(t, err, ErrInvalidPolicy)
}

func TestPolicyRoundTripPreservesProposedCondition(t *testing.T) {
	policy := Policy{
		APIVersion: PolicyAPIVersion, Kind: PolicyKind,
		Metadata: PolicyMetadata{Code: "member-target-status", Name: "Member target status"},
		Scope:    PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec: PolicySpec{Resource: "tenant.member", Actions: []string{"update"}, Condition: `resource.status == "disabled"`,
			ProposedCondition: `proposed.status == "active"`, Effect: EffectAllow},
	}
	document, err := MarshalPolicy(policy)
	require.NoError(t, err)
	parsed, err := ParsePolicy(document)
	require.NoError(t, err)
	require.Equal(t, policy.Spec.ProposedCondition, parsed.Spec.ProposedCondition)
}
