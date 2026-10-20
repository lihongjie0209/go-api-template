package authorization

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestPlatformGrantGetIncludesPrincipalIdentifier(t *testing.T) {
	t.Parallel()
	const grantID = "f833091d-3f48-4fe6-b46d-c439112cbdaa"
	tests := []struct {
		name       string
		principal  string
		display    string
		identifier string
	}{
		{name: "user", principal: "user", display: "同名用户", identifier: "first.user"},
		{name: "service account", principal: "service_account", display: "同名服务", identifier: "client-one"},
		{name: "deleted principal", principal: "user", display: "18f4762f-f4f9-47d5-bd7a-6f07af436f58", identifier: "18f4762f-f4f9-47d5-bd7a-6f07af436f58"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			service := NewPlatformGrantService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil)
			stamp := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
			mock.ExpectQuery(`(?s)SELECT .*COALESCE\(u.username,sa.client_id,g.principal_id\) AS principal_identifier.*WHERE g.id=\?`).
				WithArgs(grantID).
				WillReturnRows(sqlmock.NewRows([]string{"id", "principal_type", "principal_id", "principal_name", "principal_identifier", "permission_id", "permission_key", "name", "created_at", "created_by", "updated_at", "updated_by", "version"}).
					AddRow(grantID, tt.principal, "18f4762f-f4f9-47d5-bd7a-6f07af436f58", tt.display, tt.identifier, "08896c3a-4c82-473e-90c8-b59f60446139", "platform.member.read", "查看成员", stamp, "system", stamp, "system", 1))
			grant, err := service.Get(t.Context(), grantID)
			require.NoError(t, err)
			require.Equal(t, tt.identifier, grant.PrincipalIdentifier)
			require.Equal(t, tt.display, grant.PrincipalName)
			encoded, err := json.Marshal(grant)
			require.NoError(t, err)
			require.Contains(t, string(encoded), `"principal_identifier":"`+tt.identifier+`"`)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPlatformGrantPageIncludesPrincipalIdentifier(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := NewPlatformGrantService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil)
	const principalID = "18f4762f-f4f9-47d5-bd7a-6f07af436f58"
	stamp := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT count\(\*\) FROM platform_principal_permissions`).
		WithArgs(principalID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT .*COALESCE\(u.username,sa.client_id,g.principal_id\) AS principal_identifier.*WHERE .*ORDER BY g.created_at DESC`).
		WithArgs(principalID, 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "principal_type", "principal_id", "principal_name", "principal_identifier", "permission_id", "permission_key", "name", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow("f833091d-3f48-4fe6-b46d-c439112cbdaa", "user", principalID, "同名用户", "first.user", "08896c3a-4c82-473e-90c8-b59f60446139", "platform.member.read", "查看成员", stamp, "system", stamp, "system", 1))
	page, err := service.Page(t.Context(), PlatformGrantPageInput{PrincipalID: principalID})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Len(t, page.Items, 1)
	require.Equal(t, "first.user", page.Items[0].PrincipalIdentifier)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPlatformGrantValidation(t *testing.T) {
	t.Parallel()
	valid := PlatformGrantInput{PrincipalType: "user", PrincipalID: "18f4762f-f4f9-47d5-bd7a-6f07af436f58", PermissionID: "08896c3a-4c82-473e-90c8-b59f60446139"}
	require.NoError(t, validatePlatformGrant(valid))
	valid.PrincipalType = "anonymous"
	require.ErrorIs(t, validatePlatformGrant(valid), ErrPlatformGrantInvalid)
	valid.PrincipalType = "user"
	valid.PrincipalID = "*"
	require.ErrorIs(t, validatePlatformGrant(valid), ErrPlatformGrantInvalid)
}

func TestPlatformGrantRejectsUnknownPrincipalBeforeWriting(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	sqlxDB := sqlx.NewDb(db, "pgx")
	service := NewPlatformGrantService(sqlxDB, database.NewTransactor(sqlxDB), nil, nil)
	ctx := platformprincipal.SystemContext(t.Context(), "grant-admin")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.actor_id', $1, true)")).WithArgs("grant-admin").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM identity_users").WithArgs("18f4762f-f4f9-47d5-bd7a-6f07af436f58").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectRollback()
	_, err = service.Create(ctx, PlatformGrantInput{PrincipalType: "user", PrincipalID: "18f4762f-f4f9-47d5-bd7a-6f07af436f58", PermissionID: "08896c3a-4c82-473e-90c8-b59f60446139"})
	require.ErrorIs(t, err, ErrPlatformGrantInvalid)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPlatformGrantRejectsPermissionWithoutActivePlatformAction(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer raw.Close()
	sqlxDB := sqlx.NewDb(raw, "pgx")
	service := NewPlatformGrantService(sqlxDB, database.NewTransactor(sqlxDB), nil, nil)
	ctx := platformprincipal.SystemContext(t.Context(), "grant-admin")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.actor_id', $1, true)")).WithArgs("grant-admin").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM identity_users`).
		WithArgs("18f4762f-f4f9-47d5-bd7a-6f07af436f58").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT count\(\*\) FROM permissions p WHERE .*r.scope='platform'.*AND p.id=\$1`).
		WithArgs("08896c3a-4c82-473e-90c8-b59f60446139").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectRollback()
	_, err = service.Create(ctx, PlatformGrantInput{
		PrincipalType: "user",
		PrincipalID:   "18f4762f-f4f9-47d5-bd7a-6f07af436f58",
		PermissionID:  "08896c3a-4c82-473e-90c8-b59f60446139",
	})
	require.ErrorIs(t, err, ErrPlatformGrantInvalid)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageGrantablePermissionsUsesLinkedPlatformActions(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := NewPlatformGrantService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil)
	permissionID := "08896c3a-4c82-473e-90c8-b59f60446139"
	mock.ExpectQuery(`(?s)SELECT count\(\*\) FROM permissions p WHERE .*EXISTS .*l.permission_id=p.id AND l.deleted_at IS NULL.*LOWER\(p.permission_key\)`).
		WithArgs("%manage%", "%manage%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT p.id,p.permission_key,p.name FROM permissions p WHERE .*r.scope='platform'.*ORDER BY p.permission_key,p.id LIMIT \? OFFSET \?`).
		WithArgs("%manage%", "%manage%", 20, 20).
		WillReturnRows(sqlmock.NewRows([]string{"id", "permission_key", "name"}).AddRow(permissionID, "platform.member.manage", "成员管理"))
	page, err := service.PageGrantablePermissions(t.Context(), PlatformGrantablePermissionPageInput{Request: pagination.Request{Page: 2, Keyword: "manage"}})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, 2, page.Page)
	require.Equal(t, 20, page.PageSize)
	require.Equal(t, []PlatformGrantablePermission{{ID: permissionID, Key: "platform.member.manage", Name: "成员管理"}}, page.Items)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageGrantablePermissionsRejectsInvalidRequest(t *testing.T) {
	t.Parallel()
	service := NewPlatformGrantService(nil, nil, nil, nil)
	for _, input := range []PlatformGrantablePermissionPageInput{
		{Request: pagination.Request{Page: -1}},
		{Request: pagination.Request{PageSize: pagination.MaxPageSize + 1}},
		{Request: pagination.Request{Keyword: strings.Repeat("x", 257)}},
		{IDs: []string{"not-a-uuid"}},
		{IDs: make([]string, 201)},
	} {
		_, err := service.PageGrantablePermissions(t.Context(), input)
		require.ErrorIs(t, err, ErrPlatformGrantInvalid)
	}
}

func TestPageGrantablePermissionsPropagatesDatabaseFailure(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := NewPlatformGrantService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil)
	databaseError := errors.New("database unavailable")
	mock.ExpectQuery(`SELECT count\(\*\) FROM permissions p WHERE`).
		WillReturnError(databaseError)
	_, err = service.PageGrantablePermissions(t.Context(), PlatformGrantablePermissionPageInput{})
	require.ErrorIs(t, err, databaseError)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPageGrantablePermissionsFiltersSelectedIDs(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := NewPlatformGrantService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil)
	first := "08896c3a-4c82-473e-90c8-b59f60446139"
	second := "18f4762f-f4f9-47d5-bd7a-6f07af436f58"
	mock.ExpectQuery(`(?s)SELECT count\(\*\) FROM permissions p WHERE .*r.scope='platform'.*p.id IN \(\?, \?\).*LOWER\(p.permission_key\)`).
		WithArgs(first, second, "%manage%", "%manage%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT p.id,p.permission_key,p.name FROM permissions p WHERE .*p.id IN \(\?, \?\).*ORDER BY p.permission_key,p.id LIMIT \? OFFSET \?`).
		WithArgs(first, second, "%manage%", "%manage%", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "permission_key", "name"}).AddRow(first, "platform.member.manage", "成员管理"))
	page, err := service.PageGrantablePermissions(t.Context(), PlatformGrantablePermissionPageInput{
		Request: pagination.Request{Keyword: "manage"}, IDs: []string{first, second},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, first, page.Items[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}
