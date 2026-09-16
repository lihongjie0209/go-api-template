package tenant

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

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
		{Request: pagination.Request{Page: 1}, Statuses: []Status{"unknown"}},
		{Request: pagination.Request{Page: 1}, CreatedAtFrom: &from, CreatedAtTo: &to},
	}
	for _, input := range tests {
		if _, err := service.Page(ctx, input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Page() error = %v, want ErrInvalid", err)
		}
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
	mock.ExpectQuery(`FROM tenant_memberships WHERE tenant_id=\? AND id=\?`).WithArgs("tenant-a", "member-b").WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "user_id", "username", "display_name", "status", "joined_at", "created_at", "created_by", "updated_at", "updated_by", "version"}))
	_, err = NewRepository(db).GetMember(t.Context(), "tenant-a", "member-b")
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
