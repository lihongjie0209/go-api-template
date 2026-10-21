package grpctransport

import (
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	identityv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/identity/v1"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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

	response, err := server.ListUsers(t.Context(), &identityv1.ListUsersRequest{Keyword: "=Alice", Status: identityv1.UserStatus_USER_STATUS_ACTIVE})
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
			response, err := server.ListUsers(t.Context(), &identityv1.ListUsersRequest{Keyword: "=alice", Status: identityv1.UserStatus_USER_STATUS_ACTIVE})
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
	_, err = server.ListUsers(t.Context(), &identityv1.ListUsersRequest{Keyword: "=alice", Status: identityv1.UserStatus_USER_STATUS_ACTIVE})
	if status.Code(err) != codes.Internal {
		t.Fatalf("database failure code=%v, want internal", status.Code(err))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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
	ctx := platformprincipal.SystemContext(t.Context(), "identity-batch-test")
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
