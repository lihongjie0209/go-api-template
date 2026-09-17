package pbac

import (
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func lifecycleTestService(t *testing.T) (*LifecycleService, sqlmock.Sqlmock, *sqlx.DB) {
	t.Helper()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := NewLifecycleService(
		NewRepository(db),
		database.NewTransactor(db),
		testRegistry(t),
		nil,
		nil,
		nil,
	)
	return service, mock, db
}

func tenantPolicyDocument() Policy {
	authenticated := true
	return Policy{
		APIVersion: APIVersionV1,
		Kind:       KindPolicy,
		Metadata: PolicyMetadata{
			Code: "department-manager-update-member",
			Name: "Department manager updates member",
		},
		Scope: PolicyScope{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec: PolicySpec{
			Subject: SubjectMatcher{
				Authenticated: &authenticated,
				Roles:         RolesMatcher{AnyOf: []string{"department_manager"}},
			},
			Resource: ResourceMatcher{Type: "member"},
			Actions:  []string{"update"},
			When:     "environment.business_day",
			Effect:   EffectAllow,
		},
	}
}

func tenantActorContext(t *testing.T, tenantID string) platformprincipal.Principal {
	t.Helper()
	return platformprincipal.Principal{ID: "admin-1", Type: platformprincipal.TypeUser, TenantID: tenantID}
}

func TestLifecycleCreatePersistsPolicyDraftAtomically(t *testing.T) {
	t.Parallel()
	service, mock, _ := lifecycleTestService(t)
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO pbac_policies").
		WithArgs(sqlmock.AnyArg(), "department-manager-update-member", "Department manager updates member", "", PolicyScopeTenant, "tenant-1", PolicyStatusActive, sqlmock.AnyArg(), "admin-1", sqlmock.AnyArg(), "admin-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO pbac_policy_versions").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), int64(1), sqlmock.AnyArg(), VersionStatusDraft, sqlmock.AnyArg(), "admin-1", sqlmock.AnyArg(), "admin-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO pbac_policy_actions").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "member", "update", sqlmock.AnyArg(), "admin-1", sqlmock.AnyArg(), "admin-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT "+regexp.QuoteMeta(policyColumns)+" FROM pbac_policies").
		WithArgs(sqlmock.AnyArg(), "tenant-1").
		WillReturnRows(sqlmock.NewRows(policyColumnNames()).AddRow(
			"policy-1", "department-manager-update-member", "Department manager updates member", "",
			"tenant", "tenant-1", nil, "active", now, "admin-1", now, "admin-1", 1,
		))
	mock.ExpectQuery("SELECT "+regexp.QuoteMeta(prefixedVersionColumns("v"))).
		WithArgs(sqlmock.AnyArg(), int64(1), "tenant-1").
		WillReturnRows(sqlmock.NewRows(versionColumnNames()).AddRow(
			"version-1", "policy-1", 1, "document", "draft", nil, nil,
			now, "admin-1", now, "admin-1", 1,
		))

	ctx := platformprincipal.WithContext(t.Context(), tenantActorContext(t, "tenant-1"))
	result, err := service.Create(ctx, CreatePolicyInput{Document: tenantPolicyDocument()})
	require.NoError(t, err)
	require.Equal(t, "policy-1", result.Policy.ID)
	require.Equal(t, VersionStatusDraft, result.Version.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLifecycleCreateRejectsAnotherTenantBeforeTransaction(t *testing.T) {
	t.Parallel()
	service, mock, _ := lifecycleTestService(t)
	ctx := platformprincipal.WithContext(t.Context(), tenantActorContext(t, "tenant-2"))
	_, err := service.Create(ctx, CreatePolicyInput{Document: tenantPolicyDocument()})
	require.ErrorIs(t, err, ErrTenantAccessDenied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLifecycleCreateVersionRollsBackOnOptimisticConflict(t *testing.T) {
	t.Parallel()
	service, mock, _ := lifecycleTestService(t)
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT "+regexp.QuoteMeta(policyColumns)+" FROM pbac_policies").
		WithArgs("policy-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows(policyColumnNames()).AddRow(
			"policy-1", "department-manager-update-member", "Department manager updates member", "",
			"tenant", "tenant-1", nil, "active", now, "admin-1", now, "admin-1", 3,
		))
	mock.ExpectRollback()

	ctx := platformprincipal.WithContext(t.Context(), tenantActorContext(t, "tenant-1"))
	_, err := service.CreateVersion(ctx, CreateVersionInput{
		PolicyID: "policy-1", ExpectedPolicyVersion: 2, Document: tenantPolicyDocument(),
	})
	require.ErrorIs(t, err, ErrPolicyConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLifecyclePublishArchivesAndSwitchesInOneTransaction(t *testing.T) {
	t.Parallel()
	service, mock, _ := lifecycleTestService(t)
	now := time.Now()
	document, err := MarshalPolicy(tenantPolicyDocument())
	require.NoError(t, err)
	publishedVersion := int64(1)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT "+regexp.QuoteMeta(policyColumns)+" FROM pbac_policies").
		WithArgs("policy-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows(policyColumnNames()).AddRow(
			"policy-1", "department-manager-update-member", "Department manager updates member", "",
			"tenant", "tenant-1", publishedVersion, "active", now, "admin-1", now, "admin-1", 2,
		))
	mock.ExpectQuery("SELECT "+regexp.QuoteMeta(versionColumns)+" FROM pbac_policy_versions").
		WithArgs("policy-1", int64(2)).
		WillReturnRows(sqlmock.NewRows(versionColumnNames()).AddRow(
			"version-2", "policy-1", 2, string(document), "draft", nil, nil,
			now, "admin-1", now, "admin-1", 1,
		))
	mock.ExpectExec("UPDATE pbac_policy_versions.*status='archived'").
		WithArgs(sqlmock.AnyArg(), "admin-1", "policy-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE pbac_policy_versions.*status='published'").
		WithArgs(sqlmock.AnyArg(), "admin-1", sqlmock.AnyArg(), "admin-1", "version-2", "policy-1", int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE pbac_policies.*published_version_number").
		WithArgs(int64(2), sqlmock.AnyArg(), "admin-1", "policy-1", int64(2), "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT "+regexp.QuoteMeta(policyColumns)+" FROM pbac_policies").
		WithArgs("policy-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows(policyColumnNames()).AddRow(
			"policy-1", "department-manager-update-member", "Department manager updates member", "",
			"tenant", "tenant-1", int64(2), "active", now, "admin-1", now, "admin-1", 3,
		))
	mock.ExpectQuery("SELECT "+regexp.QuoteMeta(prefixedVersionColumns("v"))).
		WithArgs("policy-1", int64(2), "tenant-1").
		WillReturnRows(sqlmock.NewRows(versionColumnNames()).AddRow(
			"version-2", "policy-1", 2, string(document), "published", now, "admin-1",
			now, "admin-1", now, "admin-1", 2,
		))

	ctx := platformprincipal.WithContext(t.Context(), tenantActorContext(t, "tenant-1"))
	result, err := service.Publish(ctx, PublishInput{PolicyID: "policy-1", VersionNumber: 2, ExpectedPolicyVersion: 2})
	require.NoError(t, err)
	require.Equal(t, int64(2), *result.Policy.PublishedVersionNumber)
	require.Equal(t, VersionStatusPublished, result.Version.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryLoadAllPublished(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	repository := NewRepository(sqlx.NewDb(raw, "sqlmock"))
	document, err := MarshalPolicy(tenantPolicyDocument())
	require.NoError(t, err)
	mock.ExpectQuery("SELECT p.code,p.scope,p.tenant_id,v.document").
		WillReturnRows(sqlmock.NewRows([]string{"code", "scope", "tenant_id", "document"}).
			AddRow("department-manager-update-member", "tenant", "tenant-1", string(document)))

	policies, err := repository.LoadAllPublished(t.Context())
	require.NoError(t, err)
	require.Len(t, policies, 1)
	require.Equal(t, "department-manager-update-member", policies[0].Metadata.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLifecycleSetStatusUsesOptimisticLockAndTenantFilter(t *testing.T) {
	t.Parallel()
	service, mock, _ := lifecycleTestService(t)
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT "+regexp.QuoteMeta(policyColumns)+" FROM pbac_policies").
		WithArgs("policy-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows(policyColumnNames()).AddRow(
			"policy-1", "department-manager-update-member", "Department manager updates member", "",
			"tenant", "tenant-1", int64(1), "active", now, "admin-1", now, "admin-1", 4,
		))
	mock.ExpectExec("UPDATE pbac_policies SET status=").
		WithArgs(PolicyStatusDisabled, sqlmock.AnyArg(), "admin-1", "policy-1", int64(4), "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT "+regexp.QuoteMeta(policyColumns)+" FROM pbac_policies").
		WithArgs("policy-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows(policyColumnNames()).AddRow(
			"policy-1", "department-manager-update-member", "Department manager updates member", "",
			"tenant", "tenant-1", int64(1), "disabled", now, "admin-1", now, "admin-1", 5,
		))

	ctx := platformprincipal.WithContext(t.Context(), tenantActorContext(t, "tenant-1"))
	record, err := service.SetStatus(ctx, SetPolicyStatusInput{
		PolicyID: "policy-1", Status: PolicyStatusDisabled, ExpectedPolicyVersion: 4,
	})
	require.NoError(t, err)
	require.Equal(t, PolicyStatusDisabled, record.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRuntimeLoaderReplacesEngineFromPublishedState(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	repository := NewRepository(sqlx.NewDb(raw, "sqlmock"))
	engine, err := NewEngine(testRegistry(t), nil)
	require.NoError(t, err)
	document, err := MarshalPolicy(tenantPolicyDocument())
	require.NoError(t, err)
	mock.ExpectQuery("SELECT p.code,p.scope,p.tenant_id,v.document").
		WillReturnRows(sqlmock.NewRows([]string{"code", "scope", "tenant_id", "document"}).
			AddRow("department-manager-update-member", "tenant", "tenant-1", string(document)))

	require.NoError(t, NewRuntimeLoader(repository, engine).Refresh(t.Context()))
	decision, err := engine.Evaluate(t.Context(), EvaluationRequest{
		Subject: Subject{
			Authenticated: true, TenantID: "tenant-1", Roles: []string{"department_manager"},
		},
		Resource: Resource{Type: "member", TenantID: "tenant-1"},
		Action:   "update",
		Context:  OperationContext{BusinessDay: true},
	})
	require.NoError(t, err)
	require.Equal(t, DecisionEffectAllow, decision.Effect)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthorizePolicyTenant(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		actor   platformprincipal.Principal
		scope   PolicyScope
		allowed bool
	}{
		{name: "platform actor manages global", actor: platformprincipal.Principal{ID: "platform-admin"}, scope: PolicyScope{Type: PolicyScopeGlobal}, allowed: true},
		{name: "tenant actor manages own tenant", actor: platformprincipal.Principal{ID: "tenant-admin", TenantID: "tenant-1"}, scope: PolicyScope{Type: PolicyScopeTenant, TenantID: "tenant-1"}, allowed: true},
		{name: "tenant actor cannot manage global", actor: platformprincipal.Principal{ID: "tenant-admin", TenantID: "tenant-1"}, scope: PolicyScope{Type: PolicyScopeGlobal}},
		{name: "tenant actor cannot manage another tenant", actor: platformprincipal.Principal{ID: "tenant-admin", TenantID: "tenant-1"}, scope: PolicyScope{Type: PolicyScopeTenant, TenantID: "tenant-2"}},
		{name: "platform actor cannot impersonate tenant", actor: platformprincipal.Principal{ID: "platform-admin"}, scope: PolicyScope{Type: PolicyScopeTenant, TenantID: "tenant-1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := authorizePolicyTenant(test.actor, test.scope)
			if test.allowed {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrTenantAccessDenied)
		})
	}
}

func policyColumnNames() []string {
	return []string{"id", "code", "name", "description", "scope", "tenant_id", "published_version_number", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}
}

func versionColumnNames() []string {
	return []string{"id", "policy_id", "version_number", "document", "status", "published_at", "published_by", "created_at", "created_by", "updated_at", "updated_by", "version"}
}
