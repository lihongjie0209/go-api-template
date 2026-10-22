package permission

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type unavailablePermissionActors struct{}

func (unavailablePermissionActors) ResolveUserIDs(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("identity unavailable")
}

func permissionRows(id string, version int64, detail bool) *sqlmock.Rows {
	columns := []string{"id", "parent_id", "permission_key", "name", "node_type", "resource", "action", "description", "sort_order", "status", "is_system", "created_at", "created_by", "updated_at", "updated_by", "version"}
	values := []driver.Value{id, nil, "platform.menu", "Menu", "group", "", "", "", 0, "active", false, time.Now(), "creator", time.Now(), "updater", version}
	if detail {
		columns = append(columns, "parent_name")
		values = append(values, "")
	}
	return sqlmock.NewRows(columns).AddRow(values...)
}

func actionPermissionRows(id string, version int64, detail bool) *sqlmock.Rows {
	columns := []string{"id", "parent_id", "permission_key", "name", "node_type", "resource", "action", "description", "sort_order", "status", "is_system", "created_at", "created_by", "updated_at", "updated_by", "version"}
	values := []driver.Value{id, nil, "platform.user.read", "Read user", "permission", "identity.user", "read", "", 0, "active", false, time.Now(), "creator", time.Now(), "updater", version}
	if detail {
		columns = append(columns, "parent_name")
		values = append(values, "")
	}
	return sqlmock.NewRows(columns).AddRow(values...)
}

func TestPermissionGroupCreateSurvivesActorDisplayOutage(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := New(NewRepository(db), database.NewTransactor(db), nil, nil, unavailablePermissionActors{}, nil, nil)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM permissions WHERE deleted_at IS NULL ORDER BY`).WillReturnRows(sqlmock.NewRows([]string{"id", "parent_id", "permission_key", "name", "node_type", "resource", "action", "description", "sort_order", "status", "is_system", "created_at", "created_by", "updated_at", "updated_by", "version"}))
	mock.ExpectExec(`INSERT INTO permissions`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT p.id,p.parent_id`).WithArgs(sqlmock.AnyArg()).WillReturnRows(permissionRows("permission-1", 1, true))
	mock.ExpectCommit()
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	created, err := service.Create(ctx, Input{Key: "platform.menu", Name: "Menu", NodeType: "group", Status: "active"})
	require.NoError(t, err)
	require.EqualValues(t, 1, created.Version)
	require.Equal(t, "creator", created.CreatedByName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPermissionDeleteChecksVersionDespiteActorDisplayOutage(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := New(NewRepository(db), database.NewTransactor(db), nil, nil, unavailablePermissionActors{}, nil, nil)
	mock.ExpectQuery(`SELECT p.id,p.parent_id`).WithArgs("permission-1").WillReturnRows(permissionRows("permission-1", 4, true))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM permissions WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).WithArgs("permission-1").WillReturnRows(permissionRows("permission-1", 4, false))
	mock.ExpectRollback()
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	err = service.Delete(ctx, "permission-1", 3)
	require.ErrorIs(t, err, ErrStaleVersion)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPermissionGroupUpdateSurvivesActorDisplayOutage(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := New(NewRepository(db), database.NewTransactor(db), nil, nil, unavailablePermissionActors{}, nil, nil)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM permissions WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).WithArgs("permission-1").WillReturnRows(permissionRows("permission-1", 4, false))
	mock.ExpectQuery(`SELECT .* FROM permissions WHERE deleted_at IS NULL ORDER BY`).WillReturnRows(permissionRows("permission-1", 4, false))
	mock.ExpectExec(`UPDATE permissions SET parent_id=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT p.id,p.parent_id`).WithArgs("permission-1").WillReturnRows(permissionRows("permission-1", 5, true))
	mock.ExpectCommit()
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	updated, err := service.Update(ctx, "permission-1", 4, Input{Key: "platform.menu", Name: "Menu", NodeType: "group", Status: "active"})
	require.NoError(t, err)
	require.EqualValues(t, 5, updated.Version)
	require.Equal(t, "updater", updated.UpdatedByName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPermissionSeedSurvivesActorDisplayOutage(t *testing.T) {
	t.Parallel()
	id, err := SeedID("platform.menu")
	require.NoError(t, err)
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := New(NewRepository(db), database.NewTransactor(db), nil, nil, unavailablePermissionActors{}, nil, nil)
	mock.ExpectQuery(`SELECT .*deleted_at FROM permissions WHERE id=\?`).WithArgs(id).WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM permissions WHERE deleted_at IS NULL ORDER BY`).WillReturnRows(sqlmock.NewRows([]string{"id", "parent_id", "permission_key", "name", "node_type", "resource", "action", "description", "sort_order", "status", "is_system", "created_at", "created_by", "updated_at", "updated_by", "version"}))
	mock.ExpectExec(`INSERT INTO permissions`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT p.id,p.parent_id`).WithArgs(id).WillReturnRows(permissionRows(id, 1, true))
	mock.ExpectCommit()
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	seeded, changed, err := service.Seed(ctx, Input{Key: "platform.menu", Name: "Menu", NodeType: "group", Status: "active"})
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, id, seeded.ID)
	require.Equal(t, "creator", seeded.CreatedByName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPermissionSetActionsSurvivesActorDisplayOutage(t *testing.T) {
	t.Parallel()
	const permissionID = "08896c3a-4c82-473e-90c8-b59f60446139"
	const actionID = "18f4762f-f4f9-47d5-bd7a-6f07af436f58"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := New(NewRepository(db), database.NewTransactor(db), nil, nil, unavailablePermissionActors{}, nil, nil)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM permissions WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).WithArgs(permissionID).WillReturnRows(actionPermissionRows(permissionID, 4, false))
	mock.ExpectQuery(`SELECT r.scope FROM authorization_resources`).WithArgs("identity.user", "read").WillReturnRows(sqlmock.NewRows([]string{"scope"}).AddRow("platform"))
	mock.ExpectQuery(`SELECT a.id,r.scope,r.resource_key,a.action_key FROM authorization_resource_actions`).WithArgs(actionID).WillReturnRows(sqlmock.NewRows([]string{"id", "scope", "resource_key", "action_key"}).AddRow(actionID, "platform", "identity.user", "read"))
	mock.ExpectExec(`UPDATE permission_resource_actions SET deleted_at=CURRENT_TIMESTAMP`).WithArgs(permissionID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id,deleted_at FROM permission_resource_actions`).WithArgs(permissionID, actionID).WillReturnRows(sqlmock.NewRows([]string{"id", "deleted_at"}).AddRow("link-1", nil))
	mock.ExpectExec(`UPDATE permission_resource_actions SET deleted_at=NULL`).WithArgs("link-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE permissions SET version=version\+1`).WithArgs(permissionID, int64(4)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT p.id,p.parent_id`).WithArgs(permissionID).WillReturnRows(actionPermissionRows(permissionID, 5, true))
	mock.ExpectCommit()
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	updated, err := service.SetActions(ctx, permissionID, 4, []string{actionID})
	require.NoError(t, err)
	require.EqualValues(t, 5, updated.Version)
	require.Equal(t, "creator", updated.CreatedByName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPermissionCreateRollsBackWhenResultReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	service := New(NewRepository(db), database.NewTransactor(db), nil, nil, nil, nil, nil)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM permissions WHERE deleted_at IS NULL ORDER BY`).WillReturnRows(sqlmock.NewRows([]string{"id", "parent_id", "permission_key", "name", "node_type", "resource", "action", "description", "sort_order", "status", "is_system", "created_at", "created_by", "updated_at", "updated_by", "version"}))
	mock.ExpectExec(`INSERT INTO permissions`).WillReturnResult(sqlmock.NewResult(0, 1))
	readFailure := errors.New("transactional result read failed")
	mock.ExpectQuery(`SELECT p.id,p.parent_id`).WithArgs(sqlmock.AnyArg()).WillReturnError(readFailure)
	mock.ExpectRollback()
	_, err = service.Create(platformprincipal.SystemContext(t.Context(), "admin"), Input{Key: "platform.menu", Name: "Menu", NodeType: "group", Status: "active"})
	require.ErrorIs(t, err, readFailure)
	require.NoError(t, mock.ExpectationsWereMet())
}
