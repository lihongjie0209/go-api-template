package authorization

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestPlatformGrantActionTargetsReadMinimalProjection(t *testing.T) {
	t.Parallel()
	id := "c3719221-837d-4492-a819-592700987773"
	principalID := "0d83daa9-f20a-4eae-ad2e-2897287b2120"
	permissionID := "563a8038-7e68-42cd-9220-997b8c714e5c"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := NewPlatformGrantService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil)
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	mock.ExpectQuery(`SELECT g.id,g.principal_type,g.principal_id,.*g.version FROM platform_principal_permissions g`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "principal_type", "principal_id", "principal_name", "principal_identifier", "permission_id", "permission_key", "permission_name", "version"}).
		AddRow(id, "user", principalID, "张三", "zhangsan", permissionID, "platform.user.read", "查看用户", 7))
	mock.ExpectQuery(`SELECT g.id,g.principal_type,COALESCE.*g.version FROM platform_principal_permissions g`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"id", "principal_type", "principal_name", "principal_identifier", "permission_key", "permission_name", "version"}).
		AddRow(id, "user", "张三", "zhangsan", "platform.user.read", "查看用户", 7))
	update, err := service.UpdateTarget(ctx, id)
	require.NoError(t, err)
	require.Equal(t, PlatformGrantUpdateTarget{ID: id, PrincipalType: "user", PrincipalID: principalID, PrincipalName: "张三", PrincipalIdentifier: "zhangsan", PermissionID: permissionID, PermissionKey: "platform.user.read", PermissionName: "查看用户", Version: 7}, update)
	deletion, err := service.DeleteTarget(ctx, id)
	require.NoError(t, err)
	require.Equal(t, PlatformGrantDeleteTarget{ID: id, PrincipalType: "user", PrincipalName: "张三", PrincipalIdentifier: "zhangsan", PermissionKey: "platform.user.read", PermissionName: "查看用户", Version: 7}, deletion)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPlatformGrantActionTargetsRejectInvalidMissingAndFailedQueries(t *testing.T) {
	t.Parallel()
	id := "c3719221-837d-4492-a819-592700987773"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := NewPlatformGrantService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil)
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	_, err = service.UpdateTarget(t.Context(), id)
	require.Error(t, err)
	for _, invalid := range []string{"", "not-a-uuid"} {
		_, err = service.UpdateTarget(ctx, invalid)
		require.ErrorIs(t, err, ErrPlatformGrantInvalid)
		_, err = service.DeleteTarget(ctx, invalid)
		require.ErrorIs(t, err, ErrPlatformGrantInvalid)
	}
	mock.ExpectQuery(`SELECT g.id,g.principal_type,g.principal_id,.*g.version FROM platform_principal_permissions g`).WithArgs(id).WillReturnError(sql.ErrNoRows)
	_, err = service.UpdateTarget(ctx, id)
	require.ErrorIs(t, err, ErrPlatformGrantNotFound)
	mock.ExpectQuery(`SELECT g.id,g.principal_type,COALESCE.*g.version FROM platform_principal_permissions g`).WithArgs(id).WillReturnError(sql.ErrNoRows)
	_, err = service.DeleteTarget(ctx, id)
	require.ErrorIs(t, err, ErrPlatformGrantNotFound)
	failure := errors.New("database unavailable")
	mock.ExpectQuery(`SELECT g.id,g.principal_type,COALESCE.*g.version FROM platform_principal_permissions g`).WithArgs(id).WillReturnError(failure)
	_, err = service.DeleteTarget(ctx, id)
	require.ErrorIs(t, err, failure)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPlatformGrantDeleteRejectsStaleVersion(t *testing.T) {
	t.Parallel()
	id := "c3719221-837d-4492-a819-592700987773"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := NewPlatformGrantService(db, database.NewTransactor(db), nil, nil)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT version FROM platform_principal_permissions WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(8))
	mock.ExpectRollback()
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	err = service.Delete(ctx, id, 7)
	require.ErrorIs(t, err, ErrPlatformGrantStaleVersion)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPlatformGrantUpdateRejectsStaleVersion(t *testing.T) {
	t.Parallel()
	id := "c3719221-837d-4492-a819-592700987773"
	principalID := "0d83daa9-f20a-4eae-ad2e-2897287b2120"
	permissionID := "563a8038-7e68-42cd-9220-997b8c714e5c"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := NewPlatformGrantService(db, database.NewTransactor(db), nil, nil)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM identity_users WHERE id=\? AND status='active' AND deleted_at IS NULL`).
		WithArgs(principalID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT version FROM platform_principal_permissions WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(8))
	mock.ExpectRollback()
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	_, err = service.Update(ctx, id, 7, PlatformGrantInput{PrincipalType: "user", PrincipalID: principalID, PermissionID: permissionID})
	require.ErrorIs(t, err, ErrPlatformGrantStaleVersion)
	require.NoError(t, mock.ExpectationsWereMet())
}
