package resourcecatalog

import (
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestCatalogInputValidation(t *testing.T) {
	t.Parallel()
	valid := ResourceInput{Key: "integration.member", Name: "Member", Scope: resourceaction.ResourceScopeTenant, Status: "active"}
	require.NoError(t, validateResource(valid))
	invalid := valid
	invalid.Key = "Integration.Member"
	require.ErrorIs(t, validateResource(invalid), ErrInvalid)
	invalid = valid
	invalid.Scope = "other"
	require.ErrorIs(t, validateResource(invalid), ErrInvalid)
	action := ActionInput{ResourceID: "082c606d-f4d9-4339-8d0b-c77e5d839451", Key: "read", Name: "Read", Status: "active"}
	require.NoError(t, validateAction(action))
	action.Key = "Read"
	require.ErrorIs(t, validateAction(action), ErrInvalid)
	require.False(t, validPage(PageInput{IDs: []string{"not-uuid"}}))
	require.False(t, validPage(PageInput{Statuses: []string{"unknown"}}))
	service := &Service{}
	_, err := service.PageResources(t.Context(), PageInput{ResourceID: "082c606d-f4d9-4339-8d0b-c77e5d839451"})
	require.ErrorIs(t, err, ErrInvalid)
}

func TestCodeOwnedResourceCannotBeUpdated(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	sqlxDB := sqlx.NewDb(db, "pgx")
	service := NewService(sqlxDB, database.NewTransactor(sqlxDB), nil, nil)
	id := "082c606d-f4d9-4339-8d0b-c77e5d839451"
	ctx := platformprincipal.SystemContext(t.Context(), "test-admin")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.actor_id', $1, true)")).WithArgs("test-admin").WillReturnResult(sqlmock.NewResult(0, 1))
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT id,resource_key,name,description,scope,source,status").WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "resource_key", "name", "description", "scope", "source", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).AddRow(id, "identity.user", "User", "", "platform", "code", "active", now, "system", now, "system", 1))
	mock.ExpectRollback()
	_, err = service.UpdateResource(ctx, id, 1, ResourceInput{Key: "identity.user", Name: "Changed", Scope: resourceaction.ResourceScopePlatform, Status: "active"})
	require.True(t, errors.Is(err, ErrCodeOwned))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCatalogPageConditionsUseBoundParameters(t *testing.T) {
	where, args := pageConditions(PageInput{Request: paginationRequest("alice"), IDs: []string{"082c606d-f4d9-4339-8d0b-c77e5d839451"}, Statuses: []string{"active"}}, "a.id", "a.action_key")
	require.Contains(t, where, "a.id IN (?)")
	require.NotContains(t, where, "alice")
	require.Equal(t, []any{"%alice%", "%alice%", "082c606d-f4d9-4339-8d0b-c77e5d839451", "active"}, args)
}

func paginationRequest(keyword string) pagination.Request {
	return pagination.Request{Keyword: keyword}
}
