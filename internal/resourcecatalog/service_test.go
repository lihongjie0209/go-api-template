package resourcecatalog

import (
	"encoding/json"
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
	require.False(t, validPage(PageInput{Scope: "unknown"}))
	require.False(t, validPage(PageInput{ResourceKey: "Tenant.Member"}))
	service := &Service{}
	_, err := service.PageResources(t.Context(), PageInput{ResourceID: "082c606d-f4d9-4339-8d0b-c77e5d839451"})
	require.ErrorIs(t, err, ErrInvalid)
	_, err = service.PageResources(t.Context(), PageInput{ActiveResourcesOnly: true})
	require.ErrorIs(t, err, ErrInvalid)
	_, err = service.PageActions(t.Context(), PageInput{Scope: "unknown"})
	require.ErrorIs(t, err, ErrInvalid)
}

func TestCodeOwnedResourceCannotBeUpdated(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
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

func TestPageActionsFiltersActiveResourcesByScope(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	sqlxDB := sqlx.NewDb(db, "pgx")
	service := NewService(sqlxDB, nil, nil, nil)
	input := PageInput{
		Request:             pagination.Request{Page: 1, PageSize: 20},
		Statuses:            []string{"active"},
		ResourceKey:         "tenant.member",
		Scope:               resourceaction.ResourceScopeTenant,
		ActiveResourcesOnly: true,
	}
	where := "a.deleted_at IS NULL AND a.status IN (?) AND r.deleted_at IS NULL AND r.resource_key=? AND r.scope=? AND r.status='active'"
	mock.ExpectQuery(regexp.QuoteMeta(sqlxDB.Rebind("SELECT count(*) FROM authorization_resource_actions a JOIN authorization_resources r ON r.id=a.resource_id WHERE "+where))).WithArgs("active", "tenant.member", resourceaction.ResourceScopeTenant).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	resourceID := "082c606d-f4d9-4339-8d0b-c77e5d839451"
	actionID := "2f3c4f4d-f243-4204-a75a-20cf4b39b541"
	mock.ExpectQuery(regexp.QuoteMeta(sqlxDB.Rebind(actionSelect+" WHERE "+where+" ORDER BY r.resource_key,a.action_key,a.id LIMIT ? OFFSET ?"))).WithArgs("active", "tenant.member", resourceaction.ResourceScopeTenant, 20, 0).WillReturnRows(sqlmock.NewRows([]string{"id", "resource_id", "resource_key", "resource_name", "action_key", "name", "description", "source", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).AddRow(actionID, resourceID, "tenant.member", "租户成员", "read", "读取", "", "code", "active", now, "system", now, "system", 1))

	page, err := service.PageActions(t.Context(), input)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, resourceID, page.Items[0].ResourceID)
	require.Equal(t, "租户成员", page.Items[0].ResourceName)
	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageActionsKeywordMatchesParentResource(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	sqlxDB := sqlx.NewDb(db, "pgx")
	service := NewService(sqlxDB, nil, nil, nil)
	resourceID := "082c606d-f4d9-4339-8d0b-c77e5d839451"
	input := PageInput{
		Request:    pagination.Request{Page: 1, PageSize: 20, Keyword: "成员"},
		ResourceID: resourceID,
		Statuses:   []string{"active"},
	}
	where := `a.deleted_at IS NULL AND (LOWER(a.action_key) LIKE LOWER(?) ESCAPE '!' OR LOWER(a.name) LIKE LOWER(?) ESCAPE '!' OR LOWER(r.resource_key) LIKE LOWER(?) ESCAPE '!' OR LOWER(r.name) LIKE LOWER(?) ESCAPE '!') AND a.status IN (?) AND r.deleted_at IS NULL AND a.resource_id=?`
	countSQL := `SELECT count(*) FROM authorization_resource_actions a JOIN authorization_resources r ON r.id=a.resource_id WHERE ` + where
	mock.ExpectQuery(regexp.QuoteMeta(sqlxDB.Rebind(countSQL))).WithArgs("%成员%", "%成员%", "%成员%", "%成员%", "active", resourceID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta(sqlxDB.Rebind(actionSelect+` WHERE `+where+` ORDER BY r.resource_key,a.action_key,a.id LIMIT ? OFFSET ?`))).WithArgs("%成员%", "%成员%", "%成员%", "%成员%", "active", resourceID, 20, 0).WillReturnRows(sqlmock.NewRows([]string{"id", "resource_id", "resource_key", "resource_name", "action_key", "name", "description", "source", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}))

	page, err := service.PageActions(t.Context(), input)
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.NotNil(t, page.Items)
	require.Zero(t, page.Total)
	encoded, err := json.Marshal(page)
	require.NoError(t, err)
	require.JSONEq(t, `{"items":[],"page":1,"page_size":20,"total":0}`, string(encoded))
	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageResourcesEmptyResultUsesArrayContract(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	sqlxDB := sqlx.NewDb(db, "pgx")
	service := NewService(sqlxDB, nil, nil, nil)
	mock.ExpectQuery(`SELECT count\(\*\) FROM authorization_resources WHERE deleted_at IS NULL`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT id,resource_key,name,description,scope,source,status`).WithArgs(20, 0).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	page, err := service.PageResources(t.Context(), PageInput{Request: pagination.Request{Page: 1, PageSize: 20}})
	require.NoError(t, err)
	encoded, err := json.Marshal(page)
	require.NoError(t, err)
	require.JSONEq(t, `{"items":[],"page":1,"page_size":20,"total":0}`, string(encoded))
	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetActionIncludesResourceDisplayName(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	sqlxDB := sqlx.NewDb(db, "pgx")
	service := NewService(sqlxDB, nil, nil, nil)
	actionID := "2f3c4f4d-f243-4204-a75a-20cf4b39b541"
	resourceID := "082c606d-f4d9-4339-8d0b-c77e5d839451"
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT .*r\.name AS resource_name.*WHERE a\.id=\$1 AND a\.deleted_at IS NULL AND r\.deleted_at IS NULL`).WithArgs(actionID).WillReturnRows(sqlmock.NewRows([]string{"id", "resource_id", "resource_key", "resource_name", "action_key", "name", "description", "source", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).AddRow(actionID, resourceID, "tenant.member", "租户成员", "read", "读取", "", "code", "active", now, "system", now, "system", 1))

	action, err := service.GetAction(t.Context(), actionID)
	require.NoError(t, err)
	require.Equal(t, resourceID, action.ResourceID)
	require.Equal(t, "tenant.member", action.ResourceKey)
	require.Equal(t, "租户成员", action.ResourceName)
	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCatalogCreateResourceRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := NewService(db, database.NewTransactor(db), nil, nil)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.actor_id', $1, true)")).WithArgs("admin").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO authorization_resources`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id,resource_key,name,description,scope,source,status,created_at,created_by,updated_at,updated_by,version FROM authorization_resources WHERE id=`).
		WithArgs(sqlmock.AnyArg()).WillReturnError(errors.New("result read unavailable"))
	mock.ExpectRollback()

	_, err = service.CreateResource(platformprincipal.SystemContext(t.Context(), "admin"), ResourceInput{Key: "catalog.resource", Name: "Resource", Scope: resourceaction.ResourceScopePlatform, Status: "active"})
	require.ErrorContains(t, err, "result read unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCatalogCreateActionRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := NewService(db, database.NewTransactor(db), nil, nil)
	resourceID := "082c606d-f4d9-4339-8d0b-c77e5d839451"
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.actor_id', $1, true)")).WithArgs("admin").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id,resource_key,name,description,scope,source,status.*FROM authorization_resources WHERE id=`).
		WithArgs(resourceID).WillReturnRows(sqlmock.NewRows([]string{"id", "resource_key", "name", "description", "scope", "source", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow(resourceID, "catalog.resource", "Resource", "", "platform", "manual", "active", now, "admin", now, "admin", 1))
	mock.ExpectExec(`INSERT INTO authorization_resource_actions`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT a.id,a.resource_id,r.resource_key,r.name AS resource_name.*WHERE a.id=`).
		WithArgs(sqlmock.AnyArg()).WillReturnError(errors.New("result read unavailable"))
	mock.ExpectRollback()

	_, err = service.CreateAction(platformprincipal.SystemContext(t.Context(), "admin"), ActionInput{ResourceID: resourceID, Key: "read", Name: "Read", Status: "active"})
	require.ErrorContains(t, err, "result read unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCatalogUpdateResourceRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := NewService(db, database.NewTransactor(db), nil, nil)
	id := "082c606d-f4d9-4339-8d0b-c77e5d839451"
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.actor_id', $1, true)")).WithArgs("admin").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id,resource_key,name,description,scope,source,status.*FROM authorization_resources WHERE id=`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "resource_key", "name", "description", "scope", "source", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow(id, "catalog.resource", "Resource", "", "platform", "manual", "active", now, "admin", now, "admin", 2))
	mock.ExpectExec(`UPDATE authorization_resources SET name=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id,resource_key,name,description,scope,source,status.*FROM authorization_resources WHERE id=`).
		WithArgs(id).WillReturnError(errors.New("result read unavailable"))
	mock.ExpectRollback()

	_, err = service.UpdateResource(platformprincipal.SystemContext(t.Context(), "admin"), id, 2, ResourceInput{Key: "catalog.resource", Name: "Renamed", Scope: resourceaction.ResourceScopePlatform, Status: "active"})
	require.ErrorContains(t, err, "result read unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCatalogUpdateActionRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "pgx")
	service := NewService(db, database.NewTransactor(db), nil, nil)
	id := "2f3c4f4d-f243-4204-a75a-20cf4b39b541"
	resourceID := "082c606d-f4d9-4339-8d0b-c77e5d839451"
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.actor_id', $1, true)")).WithArgs("admin").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT a.id,a.resource_id,r.resource_key,r.name AS resource_name.*WHERE a.id=`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "resource_id", "resource_key", "resource_name", "action_key", "name", "description", "source", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow(id, resourceID, "catalog.resource", "Resource", "read", "Read", "", "manual", "active", now, "admin", now, "admin", 2))
	mock.ExpectQuery(`SELECT id,resource_key,name,description,scope,source,status.*FROM authorization_resources WHERE id=`).
		WithArgs(resourceID).WillReturnRows(sqlmock.NewRows([]string{"id", "resource_key", "name", "description", "scope", "source", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow(resourceID, "catalog.resource", "Resource", "", "platform", "manual", "active", now, "admin", now, "admin", 1))
	mock.ExpectExec(`UPDATE authorization_resource_actions SET name=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT a.id,a.resource_id,r.resource_key,r.name AS resource_name.*WHERE a.id=`).
		WithArgs(id).WillReturnError(errors.New("result read unavailable"))
	mock.ExpectRollback()

	_, err = service.UpdateAction(platformprincipal.SystemContext(t.Context(), "admin"), id, 2, ActionInput{ResourceID: resourceID, Key: "read", Name: "View", Status: "active"})
	require.ErrorContains(t, err, "result read unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func paginationRequest(keyword string) pagination.Request {
	return pagination.Request{Keyword: keyword}
}
