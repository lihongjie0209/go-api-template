package datapermission

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func dataPolicyFixture() Policy {
	authenticated := true
	return Policy{
		APIVersion: PolicyAPIVersion,
		Kind:       PolicyKind,
		Metadata:   PolicyMetadata{Code: "member-own-records", Name: "Member own records"},
		Scope:      PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec: PolicySpec{
			Subject:   pbac.SubjectMatcher{Authenticated: &authenticated},
			Resource:  "tenant.member",
			Actions:   []string{"read", "list"},
			Condition: "resource.owner_id == subject.id",
			Effect:    EffectAllow,
		},
	}
}

func dataLifecycleFixture(t *testing.T, withRuntime bool) (*LifecycleService, sqlmock.Sqlmock) {
	t.Helper()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	schema, err := NewSchema("tenant.member", map[string]Field{
		"owner_id": {Column: "tm.user_id", Type: ValueTypeText},
	})
	require.NoError(t, err)
	schemas, err := NewSchemaRegistry(schema)
	require.NoError(t, err)
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	repository := NewRepository(db)
	var runtime *RuntimeLoader
	if withRuntime {
		engine, engineErr := NewRuntimeEngine(schemas, resources)
		require.NoError(t, engineErr)
		runtime = NewRuntimeLoader(repository, engine)
	}
	return NewLifecycleService(repository, database.NewTransactor(db), schemas, resources, runtime, nil, nil), mock
}

func dataTenantContext(t *testing.T) platformprincipal.Principal {
	t.Helper()
	return platformprincipal.Principal{ID: "admin-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"}
}

func TestDataLifecycleCreatePersistsDraftAndActionsAtomically(t *testing.T) {
	t.Parallel()
	service, mock := dataLifecycleFixture(t, false)
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO data_permission_policies").
		WithArgs(sqlmock.AnyArg(), "member-own-records", "Member own records", PolicyScopeTenant, "tenant-1", StatusActive, sqlmock.AnyArg(), "admin-1", sqlmock.AnyArg(), "admin-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO data_permission_policy_versions").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), int64(1), sqlmock.AnyArg(), VersionDraft, sqlmock.AnyArg(), "admin-1", sqlmock.AnyArg(), "admin-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	for _, action := range []string{"read", "list"} {
		mock.ExpectExec("INSERT INTO data_permission_policy_actions").
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "tenant.member", action, sqlmock.AnyArg(), "admin-1", sqlmock.AnyArg(), "admin-1").
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT id,code,name,scope,tenant_id").
		WithArgs(sqlmock.AnyArg(), "tenant-1").
		WillReturnRows(policyRows().AddRow("policy-1", "member-own-records", "Member own records", "tenant", "tenant-1", nil, "active", now, "admin-1", now, "admin-1", 1))
	mock.ExpectQuery("SELECT v.id,v.policy_id,v.version_number").
		WithArgs(sqlmock.AnyArg(), int64(1), "tenant-1").
		WillReturnRows(versionRows().AddRow("version-1", "policy-1", 1, "document", "draft", nil, nil, now, "admin-1", now, "admin-1", 1))

	ctx := platformprincipal.WithContext(t.Context(), dataTenantContext(t))
	policy, version, err := service.Create(ctx, dataPolicyFixture())
	require.NoError(t, err)
	require.Equal(t, "policy-1", policy.ID)
	require.Equal(t, VersionDraft, version.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDataLifecycleSetStatusRefreshesRuntimeAfterCommit(t *testing.T) {
	t.Parallel()
	service, mock := dataLifecycleFixture(t, true)
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id,code,name,scope,tenant_id").WithArgs("policy-1", "tenant-1").
		WillReturnRows(policyRows().AddRow("policy-1", "member-own-records", "Member own records", "tenant", "tenant-1", 1, "active", now, "admin-1", now, "admin-1", 4))
	mock.ExpectExec("UPDATE data_permission_policies SET status=").
		WithArgs(StatusDisabled, sqlmock.AnyArg(), "admin-1", "policy-1", int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// Disabled policies are absent from the replacement snapshot.
	mock.ExpectQuery("SELECT p.code,p.scope,p.tenant_id,v.document").
		WillReturnRows(sqlmock.NewRows([]string{"code", "scope", "tenant_id", "document"}))
	mock.ExpectQuery("SELECT id,code,name,scope,tenant_id").WithArgs("policy-1", "tenant-1").
		WillReturnRows(policyRows().AddRow("policy-1", "member-own-records", "Member own records", "tenant", "tenant-1", 1, "disabled", now, "admin-1", now, "admin-1", 5))

	ctx := platformprincipal.WithContext(t.Context(), dataTenantContext(t))
	record, err := service.SetStatus(ctx, SetPolicyStatusInput{PolicyID: "policy-1", Status: StatusDisabled, ExpectedPolicyVersion: 4})
	require.NoError(t, err)
	require.Equal(t, StatusDisabled, record.Status)
	require.Equal(t, int64(5), record.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDataLifecyclePublishArchivesAndSwitchesAtomically(t *testing.T) {
	t.Parallel()
	service, mock := dataLifecycleFixture(t, false)
	now := time.Now()
	document, err := MarshalPolicy(dataPolicyFixture())
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id,code,name,scope,tenant_id").WithArgs("policy-1", "tenant-1").
		WillReturnRows(policyRows().AddRow("policy-1", "member-own-records", "Member own records", "tenant", "tenant-1", 1, "active", now, "admin-1", now, "admin-1", 3))
	mock.ExpectQuery("SELECT id,policy_id,version_number,document,status,version").WithArgs("policy-1", int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "policy_id", "version_number", "document", "status", "version"}).AddRow("version-2", "policy-1", 2, string(document), "draft", 1))
	mock.ExpectExec("UPDATE data_permission_policy_versions SET status='archived'").
		WithArgs(sqlmock.AnyArg(), "admin-1", "policy-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE data_permission_policy_versions SET status='published'").
		WithArgs(sqlmock.AnyArg(), "admin-1", sqlmock.AnyArg(), "admin-1", "version-2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE data_permission_policies SET published_version_number=").
		WithArgs(int64(2), sqlmock.AnyArg(), "admin-1", "policy-1", int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT id,code,name,scope,tenant_id").WithArgs("policy-1", "tenant-1").
		WillReturnRows(policyRows().AddRow("policy-1", "member-own-records", "Member own records", "tenant", "tenant-1", 2, "active", now, "admin-1", now, "admin-1", 4))
	mock.ExpectQuery("SELECT v.id,v.policy_id,v.version_number").WithArgs("policy-1", int64(2), "tenant-1").
		WillReturnRows(versionRows().AddRow("version-2", "policy-1", 2, string(document), "published", now, "admin-1", now, "admin-1", now, "admin-1", 2))

	ctx := platformprincipal.WithContext(t.Context(), dataTenantContext(t))
	record, err := service.Publish(ctx, "policy-1", 2, 3)
	require.NoError(t, err)
	require.Equal(t, int64(2), record.PublishedVersionNumber.Int64)
	require.Equal(t, int64(4), record.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDataLifecycleCreateRejectsCrossTenantPolicy(t *testing.T) {
	t.Parallel()
	service, mock := dataLifecycleFixture(t, false)
	policy := dataPolicyFixture()
	policy.Scope.TenantID = "tenant-2"
	ctx := platformprincipal.WithContext(t.Context(), dataTenantContext(t))
	_, _, err := service.Create(ctx, policy)
	require.ErrorIs(t, err, ErrPolicyScope)
	require.NoError(t, mock.ExpectationsWereMet())
}

func policyRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "code", "name", "scope", "tenant_id", "published_version_number", "status", "created_at", "created_by", "updated_at", "updated_by", "version"})
}

func versionRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "policy_id", "version_number", "document", "status", "published_at", "published_by", "created_at", "created_by", "updated_at", "updated_by", "version"})
}
