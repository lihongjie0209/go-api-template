package grpctransport

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	identityv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/identity/v1"
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
