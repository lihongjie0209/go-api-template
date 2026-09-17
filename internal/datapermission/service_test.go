package datapermission

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestServiceAuthorizesTrustedProposedObject(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	schema, err := NewSchema("tenant.department", map[string]Field{"created_by": {Column: "td.created_by", Type: ValueTypeText}})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	authenticated := true
	policy := Policy{
		APIVersion: PolicyAPIVersion, Kind: PolicyKind,
		Metadata: PolicyMetadata{Code: "department-create-own", Name: "Create department"},
		Scope:    PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec: PolicySpec{
			Subject: pbac.SubjectMatcher{Authenticated: &authenticated}, Resource: "tenant.department", Actions: []string{"create"},
			Condition: "resource.created_by == subject.id", Effect: EffectAllow,
		},
	}
	engine, err := NewEngine(schemas, resources, []Policy{policy})
	require.NoError(t, err)
	service := NewService(sqlx.NewDb(raw, "sqlmock"), engine)
	mock.ExpectQuery(`SELECT DISTINCT r.code`).WithArgs("user-1", "tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"code"}))
	mock.ExpectQuery(`SELECT DISTINCT dm.department_id`).WithArgs("user-1", "tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"department_id"}))
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"})
	ctx = accesscontrol.WithEndpoint(ctx, accesscontrol.Endpoint{Resource: "tenant.department", Action: "create", DataPermission: accesscontrol.DataPermissionObject})

	require.NoError(t, service.AuthorizeObject(ctx, "tenant.department", ResourceAttributes{"created_by": "user-1"}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestServiceObjectAuthorizationFailsClosedWithoutObjectDescriptor(t *testing.T) {
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	service := &Service{}
	require.ErrorIs(t, service.AuthorizeObject(ctx, "tenant.department", ResourceAttributes{}), ErrScopeRequired)
}

func TestServiceAuthorizesTrustedStateTransition(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	schema, err := NewSchema("tenant.member", map[string]Field{
		"owner_id": {Column: "tm.user_id", Type: ValueTypeText},
		"status":   {Column: "tm.status", Type: ValueTypeText},
	})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	policy := Policy{
		APIVersion: PolicyAPIVersion, Kind: PolicyKind,
		Metadata: PolicyMetadata{Code: "member-enable-own", Name: "Enable own membership"},
		Scope:    PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec: PolicySpec{Resource: "tenant.member", Actions: []string{"update"}, Effect: EffectAllow,
			Condition: `resource.owner_id == subject.id`, ProposedCondition: `proposed.status == "active"`},
	}
	engine, err := NewEngine(schemas, resources, []Policy{policy})
	require.NoError(t, err)
	service := NewService(sqlx.NewDb(raw, "sqlmock"), engine)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"})
	ctx = accesscontrol.WithEndpoint(ctx, accesscontrol.Endpoint{Resource: "tenant.member", Action: "update", DataPermission: accesscontrol.DataPermissionRequired})
	for range 2 {
		mock.ExpectQuery(`SELECT DISTINCT r.code`).WithArgs("user-1", "tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"code"}))
		mock.ExpectQuery(`SELECT DISTINCT dm.department_id`).WithArgs("user-1", "tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"department_id"}))
	}
	current := ResourceAttributes{"owner_id": "user-1", "status": "disabled"}
	require.NoError(t, service.AuthorizeTransition(ctx, "tenant.member", current, ResourceAttributes{"owner_id": "user-1", "status": "active"}))
	require.ErrorIs(t, service.AuthorizeTransition(ctx, "tenant.member", current, ResourceAttributes{"owner_id": "user-1", "status": "disabled"}), ErrObjectDenied)
	require.NoError(t, mock.ExpectationsWereMet())
}
