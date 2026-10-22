package datapermission

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestDataPermissionActorFallbackDoesNotLogResolverDetails(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	service := &LifecycleService{actors: &policyActorResolverStub{err: errors.New("identity token=secret-value")}}
	names := service.actorNames(t.Context(), []string{"user-1"})
	require.Equal(t, "user-1", names["user-1"])
	if strings.Contains(output.String(), "secret-value") || !strings.Contains(output.String(), "data_permission_actor_resolution_failed") {
		t.Fatalf("actor resolution log leaked dependency details or omitted safe code: %s", output.String())
	}
}

type policyActorResolverStub struct {
	called int
	ids    []string
	names  map[string]string
	err    error
}

func (stub *policyActorResolverStub) ResolveUserIDs(_ context.Context, ids []string) (map[string]string, error) {
	stub.called++
	stub.ids = append([]string{}, ids...)
	return stub.names, stub.err
}

func TestDataPolicyPresentationResolvesActorsInOneBatch(t *testing.T) {
	t.Parallel()
	resolver := &policyActorResolverStub{names: map[string]string{"user-1": "Alice", "user-2": "Bob"}}
	service := &LifecycleService{actors: resolver}
	instant := time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC)
	policies := []PolicyRecord{
		{CreatedBy: "user-1", UpdatedBy: "user-2", CreatedAt: instant, UpdatedAt: instant},
		{CreatedBy: "user-1", UpdatedBy: "system-job", CreatedAt: instant, UpdatedAt: instant},
	}
	service.presentPolicies(t.Context(), policies)
	require.Equal(t, 1, resolver.called)
	require.Equal(t, []string{"user-1", "user-2", "system-job"}, resolver.ids)
	require.Equal(t, "Alice", policies[0].CreatedByName)
	require.Equal(t, "Bob", policies[0].UpdatedByName)
	require.Equal(t, "system-job", policies[1].UpdatedByName)
	require.Equal(t, "+08:00", policies[0].CreatedAt.Format("-07:00"))

	versions := []VersionRecord{{
		CreatedBy: "user-1", UpdatedBy: "user-2",
		PublishedBy: sql.NullString{String: "user-2", Valid: true},
		PublishedAt: sql.NullTime{Time: instant, Valid: true},
		CreatedAt:   instant, UpdatedAt: instant,
	}}
	service.presentVersions(t.Context(), versions)
	require.Equal(t, 2, resolver.called)
	require.Equal(t, "Alice", versions[0].CreatedByName)
	require.Equal(t, "Bob", versions[0].PublishedByName)
	require.Equal(t, "+08:00", versions[0].PublishedAt.Time.Format("-07:00"))
	encoded, err := json.Marshal(versions[0])
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"published_by_name":"Bob"`)
}

func TestDataPolicyPresentationFallsBackAfterResolverFailure(t *testing.T) {
	t.Parallel()
	resolver := &policyActorResolverStub{err: errors.New("identity unavailable")}
	service := &LifecycleService{actors: resolver}
	records := []PolicyRecord{{CreatedBy: "user-1", UpdatedBy: "system-job"}}
	service.presentPolicies(t.Context(), records)
	require.Equal(t, "user-1", records[0].CreatedByName)
	require.Equal(t, "system-job", records[0].UpdatedByName)
}

func TestDataPolicyRecordsExposePublicationMetadata(t *testing.T) {
	t.Parallel()
	publishedAt := time.Date(2026, 10, 13, 9, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	for _, test := range []struct {
		name       string
		policy     PolicyRecord
		version    VersionRecord
		wantNumber any
		wantAt     any
		wantBy     any
	}{
		{
			name: "published",
			policy: PolicyRecord{
				PublishedVersionNumber: sql.NullInt64{Int64: 2, Valid: true},
			},
			version: VersionRecord{
				PublishedAt: sql.NullTime{Time: publishedAt, Valid: true},
				PublishedBy: sql.NullString{String: "admin-1", Valid: true},
			},
			wantNumber: float64(2),
			wantAt:     "2026-10-13T09:30:00+08:00",
			wantBy:     "admin-1",
		},
		{
			name: "draft",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			policyJSON, err := json.Marshal(test.policy)
			require.NoError(t, err)
			versionJSON, err := json.Marshal(test.version)
			require.NoError(t, err)
			var policy, version map[string]any
			require.NoError(t, json.Unmarshal(policyJSON, &policy))
			require.NoError(t, json.Unmarshal(versionJSON, &version))
			require.Contains(t, policy, "id")
			require.Contains(t, policy, "version")
			require.Contains(t, version, "id")
			require.Contains(t, version, "version_number")
			require.Contains(t, policy, "published_version_number")
			require.Equal(t, test.wantNumber, policy["published_version_number"])
			require.Contains(t, version, "published_at")
			require.Equal(t, test.wantAt, version["published_at"])
			require.Contains(t, version, "published_by")
			require.Equal(t, test.wantBy, version["published_by"])
		})
	}
}

func dataPolicyFixture() Policy {
	authenticated := true
	return Policy{
		APIVersion: PolicyAPIVersion,
		Kind:       PolicyKind,
		Metadata:   PolicyMetadata{Code: "member-own-records", Name: "Member own records"},
		Scope:      PolicyBoundary{Type: PolicyScopeTenant, TenantID: "tenant-1"},
		Spec: PolicySpec{
			Subject:   resourceaction.SubjectMatcher{Authenticated: &authenticated},
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
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	repository := NewRepository(db)
	var runtime *RuntimeLoader
	if withRuntime {
		engine, engineErr := NewRuntimeEngine(schemas, resources)
		require.NoError(t, engineErr)
		runtime = NewRuntimeLoader(repository, engine)
	}
	return NewLifecycleService(repository, database.NewTransactor(db), schemas, resources, runtime, nil, nil, nil), mock
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
	mock.ExpectQuery("SELECT id FROM tenants WHERE id=\\? AND status='active' AND deleted_at IS NULL FOR UPDATE").
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("tenant-1"))
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
	mock.ExpectQuery("SELECT id,code,name,scope,tenant_id").
		WithArgs(sqlmock.AnyArg(), "tenant-1").
		WillReturnRows(policyRows().AddRow("policy-1", "member-own-records", "Member own records", "tenant", "tenant-1", nil, "active", now, "admin-1", now, "admin-1", 1))
	mock.ExpectQuery("SELECT v.id,v.policy_id,v.version_number").
		WithArgs(sqlmock.AnyArg(), int64(1), "tenant-1").
		WillReturnRows(versionRows().AddRow("version-1", "policy-1", 1, "document", "draft", nil, nil, now, "admin-1", now, "admin-1", 1))
	mock.ExpectCommit()

	ctx := platformprincipal.WithContext(t.Context(), dataTenantContext(t))
	policy, version, err := service.Create(ctx, dataPolicyFixture())
	require.NoError(t, err)
	require.Equal(t, "policy-1", policy.ID)
	require.Equal(t, VersionDraft, version.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDataLifecycleCreateVersionReturnsAuditedRowBeforeCommit(t *testing.T) {
	t.Parallel()
	service, mock := dataLifecycleFixture(t, false)
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id,code,name,scope,tenant_id").WithArgs("policy-1", "tenant-1").
		WillReturnRows(policyRows().AddRow("policy-1", "member-own-records", "Member own records", "tenant", "tenant-1", nil, "active", now, "admin-1", now, "admin-1", 2))
	mock.ExpectQuery(`SELECT COALESCE\(max\(version_number\),0\)\+1`).WithArgs("policy-1").
		WillReturnRows(sqlmock.NewRows([]string{"next_version"}).AddRow(2))
	mock.ExpectExec("INSERT INTO data_permission_policy_versions").
		WithArgs(sqlmock.AnyArg(), "policy-1", int64(2), sqlmock.AnyArg(), VersionDraft, sqlmock.AnyArg(), "admin-1", sqlmock.AnyArg(), "admin-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	for _, action := range []string{"read", "list"} {
		mock.ExpectExec("INSERT INTO data_permission_policy_actions").
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "tenant.member", action, sqlmock.AnyArg(), "admin-1", sqlmock.AnyArg(), "admin-1").
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec("UPDATE data_permission_policies SET name=").
		WithArgs("Member own records", sqlmock.AnyArg(), "admin-1", "policy-1", int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT v.id,v.policy_id,v.version_number").WithArgs("policy-1", int64(2), "tenant-1").
		WillReturnRows(versionRows().AddRow("version-2", "policy-1", 2, "document", "draft", nil, nil, now, "admin-1", now, "admin-1", 1))
	mock.ExpectCommit()
	ctx := platformprincipal.WithContext(t.Context(), dataTenantContext(t))
	version, err := service.CreateVersion(ctx, "policy-1", 2, dataPolicyFixture())
	require.NoError(t, err)
	require.Equal(t, "version-2", version.ID)
	require.Equal(t, int64(2), version.VersionNumber)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDataLifecycleCreateRollsBackWhenAuditedReadFails(t *testing.T) {
	t.Parallel()
	service, mock := dataLifecycleFixture(t, false)
	want := errors.New("read audited policy unavailable")
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM tenants WHERE id=").WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("tenant-1"))
	mock.ExpectExec("INSERT INTO data_permission_policies").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO data_permission_policy_versions").WillReturnResult(sqlmock.NewResult(0, 1))
	for range 2 {
		mock.ExpectExec("INSERT INTO data_permission_policy_actions").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectQuery("SELECT id,code,name,scope,tenant_id").WithArgs(sqlmock.AnyArg(), "tenant-1").WillReturnError(want)
	mock.ExpectRollback()
	ctx := platformprincipal.WithContext(t.Context(), dataTenantContext(t))
	_, _, err := service.Create(ctx, dataPolicyFixture())
	require.ErrorIs(t, err, want)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDataLifecycleCreateVersionRollsBackWhenAuditedReadFails(t *testing.T) {
	t.Parallel()
	service, mock := dataLifecycleFixture(t, false)
	want := errors.New("read audited version unavailable")
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id,code,name,scope,tenant_id").WithArgs("policy-1", "tenant-1").
		WillReturnRows(policyRows().AddRow("policy-1", "member-own-records", "Member own records", "tenant", "tenant-1", nil, "active", now, "admin-1", now, "admin-1", 2))
	mock.ExpectQuery(`SELECT COALESCE\(max\(version_number\),0\)\+1`).WithArgs("policy-1").
		WillReturnRows(sqlmock.NewRows([]string{"next_version"}).AddRow(2))
	mock.ExpectExec("INSERT INTO data_permission_policy_versions").WillReturnResult(sqlmock.NewResult(0, 1))
	for range 2 {
		mock.ExpectExec("INSERT INTO data_permission_policy_actions").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec("UPDATE data_permission_policies SET name=").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT v.id,v.policy_id,v.version_number").WithArgs("policy-1", int64(2), "tenant-1").WillReturnError(want)
	mock.ExpectRollback()
	ctx := platformprincipal.WithContext(t.Context(), dataTenantContext(t))
	_, err := service.CreateVersion(ctx, "policy-1", 2, dataPolicyFixture())
	require.ErrorIs(t, err, want)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDataLifecycleCreateRequiresActiveTenant(t *testing.T) {
	databaseFailure := errors.New("tenant lookup unavailable")
	for _, test := range []struct {
		name     string
		rows     *sqlmock.Rows
		queryErr error
		wantErr  error
	}{
		{name: "missing or inactive", rows: sqlmock.NewRows([]string{"id"}), wantErr: ErrInvalidPolicy},
		{name: "database failure", queryErr: databaseFailure, wantErr: databaseFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service, mock := dataLifecycleFixture(t, false)
			mock.ExpectBegin()
			query := mock.ExpectQuery("SELECT id FROM tenants WHERE id=\\? AND status='active' AND deleted_at IS NULL FOR UPDATE").
				WithArgs("tenant-1")
			if test.queryErr != nil {
				query.WillReturnError(test.queryErr)
			} else {
				query.WillReturnRows(test.rows)
			}
			mock.ExpectRollback()

			ctx := platformprincipal.WithContext(t.Context(), dataTenantContext(t))
			_, _, err := service.Create(ctx, dataPolicyFixture())
			require.ErrorIs(t, err, test.wantErr)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
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
	mock.ExpectQuery("SELECT id,code,name,scope,tenant_id").WithArgs("policy-1", "tenant-1").
		WillReturnRows(policyRows().AddRow("policy-1", "member-own-records", "Member own records", "tenant", "tenant-1", 1, "disabled", now, "admin-1", now, "admin-1", 5))
	mock.ExpectCommit()
	// Disabled policies are absent from the replacement snapshot.
	mock.ExpectQuery("SELECT p.code,p.scope,p.tenant_id,v.document").
		WillReturnRows(sqlmock.NewRows([]string{"code", "scope", "tenant_id", "document"}))

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
	mock.ExpectQuery("SELECT id,code,name,scope,tenant_id").WithArgs("policy-1", "tenant-1").
		WillReturnRows(policyRows().AddRow("policy-1", "member-own-records", "Member own records", "tenant", "tenant-1", 2, "active", now, "admin-1", now, "admin-1", 4))
	mock.ExpectCommit()

	ctx := platformprincipal.WithContext(t.Context(), dataTenantContext(t))
	record, err := service.Publish(ctx, "policy-1", 2, 3)
	require.NoError(t, err)
	require.Equal(t, int64(2), record.PublishedVersionNumber.Int64)
	require.Equal(t, int64(4), record.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDataLifecycleLockDatabaseErrorsAreNotReportedAsNotFound(t *testing.T) {
	t.Parallel()
	want := errors.New("database temporarily unavailable")
	for _, test := range []struct {
		name  string
		run   func(context.Context, *LifecycleService) error
		setup func(sqlmock.Sqlmock)
	}{
		{
			name: "policy lock",
			run: func(ctx context.Context, service *LifecycleService) error {
				_, err := service.SetStatus(ctx, SetPolicyStatusInput{PolicyID: "policy-1", Status: StatusDisabled, ExpectedPolicyVersion: 4})
				return err
			},
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT id,code,name,scope,tenant_id").WithArgs("policy-1", "tenant-1").WillReturnError(want)
			},
		},
		{
			name: "version lock",
			run: func(ctx context.Context, service *LifecycleService) error {
				_, err := service.Publish(ctx, "policy-1", 2, 3)
				return err
			},
			setup: func(mock sqlmock.Sqlmock) {
				now := time.Now()
				mock.ExpectQuery("SELECT id,code,name,scope,tenant_id").WithArgs("policy-1", "tenant-1").
					WillReturnRows(policyRows().AddRow("policy-1", "member-own-records", "Member own records", "tenant", "tenant-1", 1, "active", now, "admin-1", now, "admin-1", 3))
				mock.ExpectQuery("SELECT id,policy_id,version_number,document,status,version").WithArgs("policy-1", int64(2)).WillReturnError(want)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service, mock := dataLifecycleFixture(t, false)
			mock.ExpectBegin()
			test.setup(mock)
			mock.ExpectRollback()
			ctx := platformprincipal.WithContext(t.Context(), dataTenantContext(t))
			err := test.run(ctx, service)
			require.ErrorIs(t, err, want)
			require.NotErrorIs(t, err, ErrPolicyNotFound)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
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
