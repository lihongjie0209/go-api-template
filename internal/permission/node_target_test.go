package permission

import (
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

func TestNodeActionTargetsProjectEditableAndConfirmationFields(t *testing.T) {
	t.Parallel()
	const id = "08896c3a-4c82-473e-90c8-b59f60446139"
	const parentID = "18f4762f-f4f9-47d5-bd7a-6f07af436f58"
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &Service{repository: NewRepository(sqlx.NewDb(raw, "sqlmock"))}
	rows := func() *sqlmock.Rows {
		now := time.Now()
		return sqlmock.NewRows([]string{
			"id", "parent_id", "parent_name", "permission_key", "name", "node_type", "resource", "action",
			"description", "sort_order", "status", "is_system", "created_at", "created_by", "updated_at", "updated_by", "version",
		}).AddRow(id, parentID, "平台", "platform.user.read", "读取用户", "permission", "identity.user", "read",
			"说明", 3, "active", false, now, "creator", now, "updater", 4)
	}
	mock.ExpectQuery(`SELECT p\.id.*FROM permissions p LEFT JOIN permissions parent`).WithArgs(id).WillReturnRows(rows())
	mock.ExpectQuery(`SELECT count\(\*\) FROM permissions WHERE parent_id=\? AND deleted_at IS NULL`).WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	update, err := service.NodeUpdateTarget(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, NodeUpdateTarget{
		ID: id, ParentID: pointer(parentID), ParentName: "平台", PermissionKey: "platform.user.read",
		Name: "读取用户", NodeType: "permission", Resource: "identity.user", Action: "read",
		Description: "说明", SortOrder: 3, Status: "active", Version: 4,
	}, update)
	mock.ExpectQuery(`SELECT p\.id.*FROM permissions p LEFT JOIN permissions parent`).WithArgs(id).WillReturnRows(rows())
	deletion, err := service.NodeDeleteTarget(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, NodeDeleteTarget{ID: id, PermissionKey: "platform.user.read", Name: "读取用户", NodeType: "permission", ParentName: "平台", Version: 4}, deletion)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestNodeActionTargetsRejectSystemMissingAndInvalidNodes(t *testing.T) {
	t.Parallel()
	const id = "08896c3a-4c82-473e-90c8-b59f60446139"
	service := &Service{}
	_, err := service.NodeUpdateTarget(t.Context(), "bad")
	require.ErrorIs(t, err, ErrInvalid)
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service.repository = NewRepository(sqlx.NewDb(raw, "sqlmock"))
	mock.ExpectQuery(`SELECT p\.id.*FROM permissions p LEFT JOIN permissions parent`).WithArgs(id).WillReturnError(sql.ErrNoRows)
	_, err = service.NodeDeleteTarget(t.Context(), id)
	require.ErrorIs(t, err, ErrNotFound)
	now := time.Now()
	mock.ExpectQuery(`SELECT p\.id.*FROM permissions p LEFT JOIN permissions parent`).WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "parent_id", "parent_name", "permission_key", "name", "node_type", "resource", "action",
			"description", "sort_order", "status", "is_system", "created_at", "created_by", "updated_at", "updated_by", "version",
		}).AddRow(id, nil, "", "platform.system", "系统权限", "group", "", "", "", 0, "active", true, now, "system", now, "system", 2))
	_, err = service.NodeUpdateTarget(t.Context(), id)
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func pointer(value string) *string { return &value }
