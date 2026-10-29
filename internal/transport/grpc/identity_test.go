package grpctransport

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	identityv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/identity/v1"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIdentityValidateSession(t *testing.T) {
	expiresAt := time.Date(2026, time.October, 22, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		request   *identityv1.ValidateSessionRequest
		rows      *sqlmock.Rows
		queryErr  error
		wantCode  codes.Code
		wantValid bool
	}{
		{name: "active", request: &identityv1.ValidateSessionRequest{SessionId: "session-1", UserId: "user-1"}, rows: sqlmock.NewRows([]string{"expires_at"}).AddRow(expiresAt), wantValid: true},
		{name: "inactive", request: &identityv1.ValidateSessionRequest{SessionId: "session-1", UserId: "user-1"}, rows: sqlmock.NewRows([]string{"expires_at"})},
		{name: "invalid input", request: &identityv1.ValidateSessionRequest{UserId: "user-1"}, wantCode: codes.InvalidArgument},
		{name: "store unavailable", request: &identityv1.ValidateSessionRequest{SessionId: "session-1", UserId: "user-1"}, queryErr: errors.New("private database error"), wantCode: codes.Unavailable},
		{name: "request canceled", request: &identityv1.ValidateSessionRequest{SessionId: "session-1", UserId: "user-1"}, queryErr: context.Canceled, wantCode: codes.Canceled},
		{name: "deadline exceeded", request: &identityv1.ValidateSessionRequest{SessionId: "session-1", UserId: "user-1"}, queryErr: context.DeadlineExceeded, wantCode: codes.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			if tt.rows != nil || tt.queryErr != nil {
				query := mock.ExpectQuery(`SELECT s.expires_at FROM identity_sessions s`).WithArgs("session-1", "user-1", sqlmock.AnyArg())
				if tt.queryErr != nil {
					query.WillReturnError(tt.queryErr)
				} else {
					query.WillReturnRows(tt.rows)
				}
			}
			server := &identityServer{auth: auth.NewWithDatabase(config.Config{}, sqlx.NewDb(raw, "sqlmock"))}
			ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "client", Type: platformprincipal.TypeServiceAccount})
			response, err := server.ValidateSession(ctx, tt.request)
			if status.Code(err) != tt.wantCode {
				t.Fatalf("code = %v, want %v: %v", status.Code(err), tt.wantCode, err)
			}
			if tt.wantCode == codes.OK {
				if response.GetValid() != tt.wantValid {
					t.Fatalf("valid = %v, want %v", response.GetValid(), tt.wantValid)
				}
				if tt.wantValid && !response.GetExpiresAt().AsTime().Equal(expiresAt) {
					t.Fatalf("expiry = %v, want %v", response.GetExpiresAt(), expiresAt)
				}
			}
			if tt.queryErr != nil && strings.Contains(err.Error(), "private database error") {
				t.Fatal("transport leaked a database error")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIdentityValidateSessionRequiresServicePrincipalBeforeLookup(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		principal platformprincipal.Principal
		want      codes.Code
	}{
		{name: "missing principal", want: codes.Unauthenticated},
		{name: "user principal", principal: platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"}, want: codes.PermissionDenied},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			if tt.principal.ID != "" {
				ctx = platformprincipal.WithContext(ctx, tt.principal)
			}
			_, err := (&identityServer{}).ValidateSession(ctx, &identityv1.ValidateSessionRequest{SessionId: "session-1", UserId: "user-1"})
			if status.Code(err) != tt.want {
				t.Fatalf("code=%v, want %v", status.Code(err), tt.want)
			}
		})
	}
}

func TestIdentityUserQueriesRequireServicePrincipal(t *testing.T) {
	t.Parallel()
	server := &identityServer{}
	methods := []struct {
		name string
		call func(context.Context) error
	}{
		{name: "get", call: func(ctx context.Context) error {
			_, err := server.GetUser(ctx, &identityv1.GetUserRequest{UserId: "user-1"})
			return err
		}},
		{name: "batch", call: func(ctx context.Context) error {
			_, err := server.BatchGetUsers(ctx, &identityv1.BatchGetUsersRequest{})
			return err
		}},
		{name: "list", call: func(ctx context.Context) error {
			_, err := server.ListUsers(ctx, &identityv1.ListUsersRequest{Keyword: "=alice"})
			return err
		}},
	}
	for _, method := range methods {
		t.Run(method.name, func(t *testing.T) {
			t.Parallel()
			for _, caller := range []struct {
				name string
				ctx  context.Context
				want codes.Code
			}{
				{name: "missing", ctx: t.Context(), want: codes.Unauthenticated},
				{name: "user", ctx: platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"}), want: codes.PermissionDenied},
			} {
				t.Run(caller.name, func(t *testing.T) {
					t.Parallel()
					if got := status.Code(method.call(caller.ctx)); got != caller.want {
						t.Fatalf("code=%v, want %v", got, caller.want)
					}
				})
			}
		})
	}
}

func TestIdentityListUsersExactModeUsesUniqueUsernameLookup(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery(regexp.QuoteMeta("WHERE LOWER(username) = ? AND deleted_at IS NULL")).WithArgs("alice").
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "status"}).AddRow("user-1", "alice", "Alice", identity.StatusActive))
	server := &identityServer{service: identity.New(identity.NewRepository(db), nil, nil, nil, nil, nil, config.Config{})}

	ctx := platformprincipal.WithContext(t.Context(), servicePrincipal())
	response, err := server.ListUsers(ctx, &identityv1.ListUsersRequest{Keyword: "=Alice", Status: identityv1.UserStatus_USER_STATUS_ACTIVE})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.GetUsers()) != 1 || response.GetUsers()[0].GetUsername() != "alice" || response.GetPage().GetTotal() != 1 {
		t.Fatalf("response = %+v", response)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityListUsersExactModeRequiresPrincipalBeforeQuery(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	server := &identityServer{service: identity.New(identity.NewRepository(sqlx.NewDb(raw, "sqlmock")), nil, nil, nil, nil, nil, config.Config{})}
	_, err = server.ListUsers(t.Context(), &identityv1.ListUsersRequest{Keyword: "=alice"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code=%v, want Unauthenticated", status.Code(err))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityListUsersExactModeIgnoresStaleActiveCache(t *testing.T) {
	tests := []struct {
		name      string
		rows      *sqlmock.Rows
		wantUsers int
	}{
		{name: "disabled", rows: sqlmock.NewRows([]string{"id", "username", "display_name", "status"}).AddRow("user-1", "alice", "Alice", identity.StatusDisabled)},
		{name: "deleted", rows: sqlmock.NewRows([]string{"id", "username", "display_name", "status"})},
		{name: "active", rows: sqlmock.NewRows([]string{"id", "username", "display_name", "status"}).AddRow("user-1", "alice", "Alice", identity.StatusActive), wantUsers: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			redisServer := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			store := cache.NewRedisStore(client)
			if err := cache.SetJSON(t.Context(), store, "identity:user:v1:username:alice", identity.User{ID: "user-1", Username: "alice", DisplayName: "Alice", Status: identity.StatusActive}, time.Minute); err != nil {
				t.Fatal(err)
			}
			mock.ExpectQuery(regexp.QuoteMeta("WHERE LOWER(username) = ? AND deleted_at IS NULL")).WithArgs("alice").WillReturnRows(test.rows)
			server := &identityServer{service: identity.New(identity.NewRepository(sqlx.NewDb(raw, "sqlmock")), nil, store, nil, nil, nil, config.Config{})}
			ctx := platformprincipal.WithContext(t.Context(), servicePrincipal())
			response, err := server.ListUsers(ctx, &identityv1.ListUsersRequest{Keyword: "=alice", Status: identityv1.UserStatus_USER_STATUS_ACTIVE})
			if err != nil {
				t.Fatal(err)
			}
			if got := len(response.GetUsers()); got != test.wantUsers {
				t.Fatalf("users=%d, want %d", got, test.wantUsers)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIdentityListUsersExactModeFailsClosedOnDatabaseError(t *testing.T) {
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := cache.NewRedisStore(client)
	if err := cache.SetJSON(t.Context(), store, "identity:user:v1:username:alice", identity.User{ID: "user-1", Username: "alice", Status: identity.StatusActive}, time.Minute); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("WHERE LOWER(username) = ? AND deleted_at IS NULL")).WithArgs("alice").WillReturnError(errors.New("database unavailable"))
	server := &identityServer{service: identity.New(identity.NewRepository(sqlx.NewDb(raw, "sqlmock")), nil, store, nil, nil, nil, config.Config{})}
	ctx := platformprincipal.WithContext(t.Context(), servicePrincipal())
	_, err = server.ListUsers(ctx, &identityv1.ListUsersRequest{Keyword: "=alice", Status: identityv1.UserStatus_USER_STATUS_ACTIVE})
	if status.Code(err) != codes.Internal {
		t.Fatalf("database failure code=%v, want internal", status.Code(err))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityErrorPreservesCancellation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		err  error
		want codes.Code
	}{
		{name: "canceled", err: fmt.Errorf("lookup user: %w", context.Canceled), want: codes.Canceled},
		{name: "deadline", err: fmt.Errorf("lookup user: %w", context.DeadlineExceeded), want: codes.DeadlineExceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := status.Code(identityError(tt.err)); got != tt.want {
				t.Fatalf("code=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestIdentityBatchGetUsersEmptyIDsReturnsNoUsersWithoutQuery(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	server := &identityServer{service: identity.New(identity.NewRepository(sqlx.NewDb(raw, "sqlmock")), nil, nil, nil, nil, nil, config.Config{})}
	ctx := platformprincipal.WithContext(t.Context(), servicePrincipal())
	response, err := server.BatchGetUsers(ctx, &identityv1.BatchGetUsersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.GetUsers()) != 0 {
		t.Fatalf("empty ID batch returned %d users", len(response.GetUsers()))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityBatchGetUsersEmptyIDsStillRequiresAuthentication(t *testing.T) {
	t.Parallel()
	server := &identityServer{}
	_, err := server.BatchGetUsers(t.Context(), &identityv1.BatchGetUsersRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("empty batch without principal code=%v, want unauthenticated", status.Code(err))
	}
}

func TestIdentityBatchGetUsersReturnsFullMaximumBatch(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	ids := make([]string, 200)
	rows := sqlmock.NewRows([]string{"id", "username", "display_name", "status"})
	for i := range ids {
		ids[i] = fmt.Sprintf("user-%03d", i)
		rows.AddRow(ids[i], fmt.Sprintf("user_%03d", i), "User", identity.StatusActive)
	}
	mock.ExpectQuery(`SELECT count\(\*\) FROM identity_users WHERE deleted_at IS NULL AND id IN`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(len(ids)))
	mock.ExpectQuery(`FROM identity_users u WHERE deleted_at IS NULL AND id IN`).WillReturnRows(rows)
	server := &identityServer{service: identity.New(identity.NewRepository(sqlx.NewDb(raw, "sqlmock")), nil, nil, nil, nil, nil, config.Config{})}
	ctx := platformprincipal.WithContext(t.Context(), servicePrincipal())
	response, err := server.BatchGetUsers(ctx, &identityv1.BatchGetUsersRequest{UserIds: ids})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(response.GetUsers()); got != len(ids) {
		t.Fatalf("batch returned %d users, want %d", got, len(ids))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityBatchGetUsersRejectsOversizedBatch(t *testing.T) {
	t.Parallel()
	server := &identityServer{}
	ctx := platformprincipal.WithContext(t.Context(), servicePrincipal())
	_, err := server.BatchGetUsers(ctx, &identityv1.BatchGetUsersRequest{UserIds: make([]string, 201)})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("oversized batch code=%v, want invalid argument", status.Code(err))
	}
}
