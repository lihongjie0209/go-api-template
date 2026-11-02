package datapermission

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestServiceRejectsCanceledSystemContext(t *testing.T) {
	ctx, cancel := context.WithCancel(platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "system", Type: platformprincipal.TypeSystem}))
	cancel()
	service := &Service{}
	_, err := service.Compile(ctx, "tenant.member")
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, service.AuthorizeObject(ctx, "tenant.member", ResourceAttributes{}), context.Canceled)
	require.ErrorIs(t, service.AuthorizeTransition(ctx, "tenant.member", ResourceAttributes{}, ResourceAttributes{}), context.Canceled)
	_, err = service.EvaluateRowActions(ctx, "tenant.member", []string{"read"}, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSubjectProjectionRequiresLiveTenantDepartment(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := NewService(sqlx.NewDb(raw, "sqlmock"), nil)
	mock.ExpectQuery(`SELECT DISTINCT r.code`).WithArgs("user-1", "tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"code"}))
	mock.ExpectQuery(regexp.QuoteMeta(`JOIN tenant_departments d ON d.id=dm.department_id AND d.tenant_id=dm.tenant_id AND d.deleted_at IS NULL`)).
		WithArgs("user-1", "tenant-1", "member-1").
		WillReturnRows(sqlmock.NewRows([]string{"department_id"}).AddRow("department-1"))

	roles, departments, err := service.projections(t.Context(), platformprincipal.Principal{
		ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1",
	})
	require.NoError(t, err)
	require.Empty(t, roles)
	require.Equal(t, []string{"department-1"}, departments)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSubjectProjectionRejectsOversizedRoleAndDepartmentSets(t *testing.T) {
	for _, overflow := range []string{"roles", "departments"} {
		t.Run(overflow, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			service := NewService(sqlx.NewDb(raw, "sqlmock"), nil)
			roleRows := sqlmock.NewRows([]string{"code"})
			if overflow == "roles" {
				for range 10_001 {
					roleRows.AddRow("role")
				}
			}
			mock.ExpectQuery(`SELECT DISTINCT r.code.*LIMIT 10001`).WithArgs("user-1", "tenant-1", "member-1").WillReturnRows(roleRows)
			if overflow == "departments" {
				departmentRows := sqlmock.NewRows([]string{"department_id"})
				for range 10_001 {
					departmentRows.AddRow("department")
				}
				mock.ExpectQuery(`SELECT DISTINCT dm.department_id.*LIMIT 10001`).WithArgs("user-1", "tenant-1", "member-1").WillReturnRows(departmentRows)
			}
			_, _, err = service.projections(t.Context(), platformprincipal.Principal{
				ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1",
			})
			require.ErrorContains(t, err, "subject projection exceeds maximum")
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestServiceAuthorizesTrustedProposedObject(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	schema, err := NewSchema("tenant.department", map[string]Field{"created_by": {Column: "td.created_by", Type: ValueTypeText}})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	authenticated := true
	policy := Policy{
		APIVersion: PolicyAPIVersion, Kind: PolicyKind,
		Metadata: PolicyMetadata{Code: "department-create-own", Name: "Create department"},
		Scope:    PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec: PolicySpec{
			Subject: resourceaction.SubjectMatcher{Authenticated: &authenticated}, Resource: "tenant.department", Actions: []string{"create"},
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
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
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

func TestServiceEvaluatesMultipleRowActionsWithOneSubjectProjection(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	schema, err := NewSchema("tenant.member", map[string]Field{"owner_id": {Column: "tm.user_id", Type: ValueTypeText}})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	engine, err := NewEngine(schemas, resources, []Policy{{
		APIVersion: PolicyAPIVersion, Kind: PolicyKind,
		Metadata: PolicyMetadata{Code: "member-own", Name: "Member own"}, Scope: PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec: PolicySpec{Resource: "tenant.member", Actions: []string{"update", "remove"}, Condition: "resource.owner_id == subject.id", Effect: EffectAllow},
	}})
	require.NoError(t, err)
	service := NewService(sqlx.NewDb(raw, "sqlmock"), engine)
	mock.ExpectQuery(`SELECT DISTINCT r.code`).WithArgs("user-1", "tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"code"}))
	mock.ExpectQuery(`SELECT DISTINCT dm.department_id`).WithArgs("user-1", "tenant-1", "member-1").WillReturnRows(sqlmock.NewRows([]string{"department_id"}))
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"})

	result, err := service.EvaluateRowActions(ctx, "tenant.member", []string{"update", "remove"}, []ResourceAttributes{{"owner_id": "user-1"}, {"owner_id": "user-2"}})
	require.NoError(t, err)
	require.Equal(t, map[string][]bool{"update": {true, false}, "remove": {true, false}}, result)
	require.NoError(t, mock.ExpectationsWereMet())
}
