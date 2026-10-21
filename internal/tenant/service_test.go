package tenant

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type failingTenantCache struct{ cache.Store }

func (failingTenantCache) Delete(context.Context, ...string) error {
	return errors.New("redis password=secret-value")
}

func TestTenantCacheInvalidationLogDoesNotExposeRedisDetails(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	service := &Service{cache: failingTenantCache{}, logger: slog.New(slog.NewJSONHandler(&output, nil))}
	service.invalidate(t.Context(), "tenant-1")
	if strings.Contains(output.String(), "secret-value") || !strings.Contains(output.String(), "tenant_cache_failed") {
		t.Fatalf("tenant cache log leaked Redis details or omitted safe code: %s", output.String())
	}
}

func TestServiceGetEnforcesTenantBoundary(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	query := regexp.QuoteMeta(`SELECT id, code, name, description, status, owner_user_id, owner_name, created_at, created_by, updated_at, updated_by, version FROM tenants WHERE id = $1 AND id = $2 AND deleted_at IS NULL`)
	mock.ExpectQuery(query).WithArgs("tenant-a", "tenant-b").WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "description", "status", "owner_user_id", "owner_name", "created_at", "created_by", "updated_at", "updated_by", "version"}))
	service := New(NewRepository(db), database.NewTransactor(db), nil, nil, nil, nil, nil, config.Config{Tenant: config.Tenant{CacheTTL: time.Minute}})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-b", Type: platformprincipal.TypeUser, TenantID: "tenant-b"})

	_, err = service.Get(ctx, "tenant-a")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestServiceCreateResolvesAuthoritativeOwner(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("identity unavailable")
	resolver := &ownerResolverStub{err: wantErr}
	service := New(&Repository{}, &database.Transactor{}, nil, nil, nil, resolver, nil, config.Config{Tenant: config.Tenant{CacheTTL: time.Minute}})
	ctx := platformprincipal.SystemContext(t.Context(), "platform-admin")
	_, err := service.Create(ctx, CreateInput{Code: "tenant-a", Name: "Tenant A", OwnerUsername: " Alice.Smith "})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Create() error=%v", err)
	}
	if resolver.username != "alice.smith" {
		t.Fatalf("resolved username=%q", resolver.username)
	}
}

func TestPlatformTenantOperationsRejectTenantContext(t *testing.T) {
	t.Parallel()
	service := New(&Repository{}, &database.Transactor{}, nil, nil, nil, &ownerResolverStub{}, nil, config.Config{})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "tenant-admin", Type: platformprincipal.TypeUser, TenantID: "tenant-a"})
	tests := []struct {
		name string
		call func() error
	}{
		{name: "create", call: func() error {
			_, err := service.Create(ctx, CreateInput{Code: "tenant-b", Name: "Tenant B", OwnerUsername: "owner"})
			return err
		}},
		{name: "get", call: func() error { _, err := service.AdminGet(ctx, "tenant-b"); return err }},
		{name: "page", call: func() error { _, err := service.AdminPage(ctx, PageInput{}); return err }},
		{name: "update", call: func() error { _, err := service.AdminUpdate(ctx, UpdateInput{}); return err }},
		{name: "delete", call: func() error { return service.AdminDelete(ctx, "tenant-b", 1) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, ErrForbidden) {
				t.Fatalf("error=%v, want ErrForbidden", err)
			}
		})
	}
}

type ownerResolverStub struct {
	username string
	err      error
}

type policyRuntimeStub struct {
	changed func(context.Context) error
}

func (s policyRuntimeStub) Changed(ctx context.Context) error { return s.changed(ctx) }

func TestAdminDeleteRefreshesPoliciesOnlyAfterCommit(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	now := time.Now()
	mock.ExpectQuery(`FROM tenants WHERE id = \? AND deleted_at IS NULL`).WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "description", "status", "owner_user_id", "owner_name", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow("tenant-1", "tenant-one", "Tenant One", "", "active", "user-1", "User One", now, "system", now, "system", 2))
	mock.ExpectBegin()
	for _, table := range []string{"data_permission_policy_actions", "data_permission_policy_versions", "data_permission_policies", "application_session_contexts", "tenant_member_roles", "tenant_department_members", "tenant_role_permissions", "tenant_roles", "tenant_administrators", "tenant_permission_grants", "tenant_application_grants", "tenant_memberships", "tenant_departments", "tenants"} {
		mock.ExpectExec("UPDATE " + table + " SET").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()
	service := New(NewRepository(db), database.NewTransactor(db), nil, nil, nil, nil, nil, config.Config{})
	called := false
	service.SetPolicyRuntime(policyRuntimeStub{changed: func(context.Context) error {
		called = true
		return mock.ExpectationsWereMet()
	}})
	if err := service.AdminDelete(platformprincipal.SystemContext(t.Context(), "system"), "tenant-1", 2); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("policy runtime was not refreshed after tenant deletion")
	}
}

func TestTenantDeletePolicyRefreshFailureIsVisible(t *testing.T) {
	want := errors.New("policy runtime unavailable")
	service := &Service{}
	service.SetPolicyRuntime(policyRuntimeStub{changed: func(context.Context) error { return want }})
	if err := service.afterTenantMutation(t.Context(), "tenant-1"); !errors.Is(err, want) {
		t.Fatalf("refresh failure = %v, want wrapped runtime error", err)
	}
}

func TestTenantDeletePolicyRefreshSurvivesClientCancellation(t *testing.T) {
	requestCtx, cancel := context.WithCancel(t.Context())
	cancel()
	service := &Service{policyTimeout: time.Second}
	service.SetPolicyRuntime(policyRuntimeStub{changed: func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			t.Fatalf("post-commit refresh inherited canceled request: %v", err)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("post-commit refresh has no deadline")
		}
		return nil
	}})
	if err := service.afterTenantMutation(requestCtx, "tenant-1"); err != nil {
		t.Fatal(err)
	}
}

func TestTenantAdminUpdateRefreshesPoliciesAfterCommit(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE tenants SET name").
		WithArgs("Tenant One", "", StatusDisabled, sqlmock.AnyArg(), "platform-admin", "tenant-1", int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	want := errors.New("policy refresh unavailable")
	service := New(NewRepository(db), database.NewTransactor(db), nil, nil, nil, nil, nil, config.Config{})
	service.SetPolicyRuntime(policyRuntimeStub{changed: func(context.Context) error {
		if err := mock.ExpectationsWereMet(); err != nil {
			return err
		}
		return want
	}})
	_, err = service.AdminUpdate(platformprincipal.SystemContext(t.Context(), "platform-admin"), UpdateInput{
		ID: "tenant-1", Name: "Tenant One", Status: StatusDisabled, Version: 2,
	})
	if !errors.Is(err, want) {
		t.Fatalf("post-commit policy refresh error = %v, want %v", err, want)
	}
}

func TestTenantScopedUpdateRefreshesPoliciesAfterCommit(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE tenants SET name").
		WithArgs("Tenant One", "", StatusDisabled, sqlmock.AnyArg(), "tenant-admin", "tenant-1", int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	want := errors.New("policy refresh unavailable")
	service := New(NewRepository(db), database.NewTransactor(db), nil, nil, nil, nil, nil, config.Config{})
	service.SetPolicyRuntime(policyRuntimeStub{changed: func(context.Context) error {
		if err := mock.ExpectationsWereMet(); err != nil {
			return err
		}
		return want
	}})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "tenant-admin", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
	_, err = service.Update(ctx, UpdateInput{ID: "tenant-1", Name: "Tenant One", Status: StatusDisabled, Version: 2})
	if !errors.Is(err, want) {
		t.Fatalf("post-commit policy refresh error = %v, want %v", err, want)
	}
}

func (r *ownerResolverStub) ResolveUsername(_ context.Context, username string) (UserSnapshot, error) {
	r.username = username
	return UserSnapshot{}, r.err
}

func TestServicePageRejectsInvalidPaginationBeforeDatabase(t *testing.T) {
	t.Parallel()
	service := New(&Repository{}, &database.Transactor{}, nil, nil, nil, nil, nil, config.Config{Tenant: config.Tenant{CacheTTL: time.Minute}})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-a", Type: platformprincipal.TypeUser, TenantID: "tenant-a"})

	_, err := service.Page(ctx, PageInput{Request: paginationRequest(1, 201)})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Page() error = %v, want ErrInvalid", err)
	}
}

func TestServicePageRejectsInvalidFiltersBeforeDatabase(t *testing.T) {
	t.Parallel()
	service := New(&Repository{}, &database.Transactor{}, nil, nil, nil, nil, nil, config.Config{Tenant: config.Tenant{CacheTTL: time.Minute}})
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-a", Type: platformprincipal.TypeUser, TenantID: "tenant-a"})
	from := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	to := from.Add(-time.Hour)
	tests := []PageInput{
		{Request: pagination.Request{Page: 1}, IDs: make([]string, 201)},
		{Request: pagination.Request{Page: 1, Keyword: string(make([]byte, maxTenantKeywordLength+1))}},
		{Request: pagination.Request{Page: 1}, IDs: []string{""}},
		{Request: pagination.Request{Page: 1}, IDs: []string{string(make([]byte, maxTenantIDLength+1))}},
		{Request: pagination.Request{Page: 1}, Codes: make([]string, 201)},
		{Request: pagination.Request{Page: 1}, Codes: []string{"invalid code"}},
		{Request: pagination.Request{Page: 1}, OwnerUserIDs: make([]string, 201)},
		{Request: pagination.Request{Page: 1}, OwnerUserIDs: []string{" "}},
		{Request: pagination.Request{Page: 1}, OwnerUserIDs: []string{string(make([]byte, maxTenantIDLength+1))}},
		{Request: pagination.Request{Page: 1}, Statuses: []Status{"unknown"}},
		{Request: pagination.Request{Page: 1}, CreatedAtFrom: &from, CreatedAtTo: &to},
	}
	for _, input := range tests {
		if _, err := service.Page(ctx, input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Page() error = %v, want ErrInvalid", err)
		}
	}
}

func TestNormalizePageInputCanonicalizesCodesAndOwnerIDs(t *testing.T) {
	t.Parallel()
	input := PageInput{Codes: []string{" Tenant_A "}, OwnerUserIDs: []string{" owner-a "}}
	if _, err := normalizePageInput(&input); err != nil {
		t.Fatal(err)
	}
	if input.Codes[0] != "tenant_a" || input.OwnerUserIDs[0] != "owner-a" {
		t.Fatalf("normalized filters = codes %v, owners %v", input.Codes, input.OwnerUserIDs)
	}
}

func TestMembershipPageRejectsInvalidFiltersBeforeDatabase(t *testing.T) {
	t.Parallel()
	service := &MembershipService{}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-a", Type: platformprincipal.TypeUser, TenantID: "tenant-a"})
	from, to := time.Now(), time.Now().Add(-time.Minute)
	for _, input := range []MemberPageInput{
		{Statuses: []Status{"unknown"}},
		{Statuses: make([]Status, 21)},
		{Request: pagination.Request{Keyword: string(make([]byte, maxTenantKeywordLength+1))}},
		{UserIDs: []string{""}},
		{Usernames: []string{string(make([]byte, 257))}},
		{JoinedFrom: &from, JoinedTo: &to},
	} {
		if _, err := service.Page(ctx, input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Page(%+v) error=%v", input, err)
		}
	}
}

func TestRepositoryGetMemberAlwaysScopesTenant(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery(`FROM tenant_memberships tm WHERE tm.tenant_id=\? AND tm.id=\?.*\(1 = 1\)`).WithArgs("tenant-a", "member-b").WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "user_id", "username", "display_name", "status", "joined_at", "created_at", "created_by", "updated_at", "updated_by", "version"}))
	_, err = NewRepository(db).GetMember(t.Context(), "tenant-a", "member-b", unrestrictedMemberScope())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMember() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
}

func TestProtectLastAdministratorCountsOnlyActiveMembers(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM tenants WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("tenant-a"))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators WHERE tenant_id=`).WithArgs("tenant-a", "member-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_administrators a JOIN tenant_memberships m.*m.status='active'`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()
	tx, err := db.BeginTxx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := protectLastAdministrator(t.Context(), tx, "tenant-a", "member-a"); !errors.Is(err, ErrConflict) {
		t.Fatalf("protectLastAdministrator() error=%v", err)
	}
	_ = tx.Rollback()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
}

type transactionalOperationRecorderStub struct {
	txErr      error
	standalone []operationlog.Entry
	txEntries  []operationlog.Entry
}

func (*transactionalOperationRecorderStub) Enabled() bool { return true }
func (r *transactionalOperationRecorderStub) Record(_ context.Context, entry operationlog.Entry) error {
	r.standalone = append(r.standalone, entry)
	return nil
}
func (r *transactionalOperationRecorderStub) RecordTx(_ context.Context, _ *sqlx.Tx, entry operationlog.Entry) error {
	r.txEntries = append(r.txEntries, entry)
	return r.txErr
}

type transactionalSecurityRecorderStub struct {
	recordErr  error
	txErr      error
	standalone []securitylog.Entry
	txEntries  []securitylog.Entry
}

func (*transactionalSecurityRecorderStub) Enabled() bool    { return true }
func (*transactionalSecurityRecorderStub) FailClosed() bool { return true }
func (r *transactionalSecurityRecorderStub) Record(_ context.Context, entry securitylog.Entry) error {
	r.standalone = append(r.standalone, entry)
	return r.recordErr
}
func (r *transactionalSecurityRecorderStub) RecordTx(_ context.Context, _ *sqlx.Tx, entry securitylog.Entry) error {
	r.txEntries = append(r.txEntries, entry)
	return r.txErr
}

func TestServiceMutationRollsBackWhenTransactionalSecurityLogFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	operations := &transactionalOperationRecorderStub{}
	wantErr := errors.New("security outbox unavailable")
	security := &transactionalSecurityRecorderStub{txErr: wantErr}
	service := &Service{transactor: database.NewTransactor(db), operations: operations, security: security}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE tenants SET name`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()

	ctx := platformprincipal.SystemContext(t.Context(), "platform-admin")
	err = service.mutate(ctx, "tenant.update", "tenant-a", map[string]any{"name": "A"}, func(tx *sqlx.Tx) error {
		_, execErr := tx.ExecContext(ctx, `UPDATE tenants SET name='A'`)
		return execErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("mutate() error = %v, want %v", err, wantErr)
	}
	if len(operations.txEntries) != 1 || len(security.txEntries) != 1 || len(operations.standalone) != 1 || len(security.standalone) != 1 {
		t.Fatalf("audit entries operation(tx=%d standalone=%d) security(tx=%d standalone=%d)", len(operations.txEntries), len(operations.standalone), len(security.txEntries), len(security.standalone))
	}
	if operations.standalone[0].Succeeded || security.standalone[0].Succeeded {
		t.Fatal("rolled-back mutation was recorded as successful")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMembershipMutationRollsBackWhenTransactionalOperationLogFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	wantErr := errors.New("operation outbox unavailable")
	operations := &transactionalOperationRecorderStub{txErr: wantErr}
	security := &transactionalSecurityRecorderStub{}
	service := &MembershipService{transactor: database.NewTransactor(db), operations: operations, security: security}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE tenant_memberships SET status`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()

	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeUser, TenantID: "tenant-a"})
	err = service.mutate(ctx, "tenant.member.status.update", "member-a", map[string]any{"status": StatusDisabled}, securitylog.Entry{EventType: securitylog.EventMembershipChanged, TenantID: "tenant-a"}, nil, func(tx *sqlx.Tx) error {
		_, execErr := tx.ExecContext(ctx, `UPDATE tenant_memberships SET status='disabled'`)
		return execErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("mutate() error = %v, want %v", err, wantErr)
	}
	if len(operations.txEntries) != 1 || len(security.txEntries) != 0 || len(operations.standalone) != 1 || len(security.standalone) != 1 {
		t.Fatalf("audit entries operation(tx=%d standalone=%d) security(tx=%d standalone=%d)", len(operations.txEntries), len(operations.standalone), len(security.txEntries), len(security.standalone))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMemberDisplayNamePrefersSnapshotAndFallsBackToUsername(t *testing.T) {
	t.Parallel()
	if got := memberDisplayName(Member{Username: "alice", DisplayName: " Alice Chen "}); got != "Alice Chen" {
		t.Fatalf("memberDisplayName() = %q", got)
	}
	if got := memberDisplayName(Member{Username: " alice "}); got != "alice" {
		t.Fatalf("memberDisplayName() fallback = %q", got)
	}
}

func paginationRequest(page, pageSize int) pagination.Request {
	return pagination.Request{Page: page, PageSize: pageSize}
}
