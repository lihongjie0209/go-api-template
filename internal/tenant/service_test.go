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
	"github.com/lihongjie0209/go-api-template/internal/pagination"
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

func paginationRequest(page, pageSize int) pagination.Request {
	return pagination.Request{Page: page, PageSize: pageSize}
}
