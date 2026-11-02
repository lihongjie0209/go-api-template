package tenant

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

func departmentMemberSyncTx(t *testing.T) (*sqlx.Tx, sqlmock.Sqlmock) {
	t.Helper()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, raw.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectBegin()
	tx, err := db.BeginTxx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx, mock
}

func departmentMemberRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "membership_id", "is_primary", "version"})
}

func TestLockActiveDepartmentMembersRejectsInactiveSelection(t *testing.T) {
	for _, test := range []struct {
		name    string
		ids     []string
		rows    *sqlmock.Rows
		wantErr bool
	}{
		{name: "empty selection", ids: nil},
		{name: "all active", ids: []string{"member-a", "member-b"}, rows: sqlmock.NewRows([]string{"id"}).AddRow("member-a").AddRow("member-b")},
		{name: "one disabled", ids: []string{"member-a", "member-b"}, rows: sqlmock.NewRows([]string{"id"}).AddRow("member-a"), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, mock := departmentMemberSyncTx(t)
			if len(test.ids) > 0 {
				mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM tenant_memberships WHERE tenant_id=? AND id IN (?, ?) AND status='active' AND deleted_at IS NULL ORDER BY id FOR UPDATE`)).
					WithArgs("tenant-1", "member-a", "member-b").WillReturnRows(test.rows)
			}
			mock.ExpectRollback()
			mock.ExpectClose()
			err := lockActiveDepartmentMembers(t.Context(), tx, "tenant-1", test.ids)
			if test.wantErr {
				require.ErrorIs(t, err, ErrInvalid)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSyncDepartmentMembersPreservesUnchangedLink(t *testing.T) {
	tx, mock := departmentMemberSyncTx(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id,membership_id,is_primary,version FROM tenant_department_members WHERE tenant_id=? AND department_id=? AND deleted_at IS NULL ORDER BY membership_id,id LIMIT 1001 FOR UPDATE`)).
		WithArgs("tenant-1", "department-1").
		WillReturnRows(departmentMemberRows().AddRow("link-1", "member-1", false, 7))
	mock.ExpectRollback()
	mock.ExpectClose()
	err := syncDepartmentMembers(t.Context(), tx, "tenant-1", "department-1", "actor-1", []DepartmentMemberAssignment{{MembershipID: "member-1"}}, time.Now())
	require.NoError(t, err)
}

func TestSyncDepartmentMembersRemovesPromotesAndRestoresLatestHistory(t *testing.T) {
	tx, mock := departmentMemberSyncTx(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id,membership_id,is_primary,version FROM tenant_department_members WHERE tenant_id=? AND department_id=? AND deleted_at IS NULL ORDER BY membership_id,id LIMIT 1001 FOR UPDATE`)).
		WithArgs("tenant-1", "department-1").
		WillReturnRows(departmentMemberRows().AddRow("link-a", "member-a", true, 3).AddRow("link-b", "member-b", false, 4))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE tenant_department_members SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND id=? AND version=? AND deleted_at IS NULL`)).
		WithArgs(sqlmock.AnyArg(), "actor-1", sqlmock.AnyArg(), "actor-1", "tenant-1", "link-a", int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE tenant_department_members SET is_primary=false,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND membership_id IN (?) AND department_id<>? AND is_primary=true AND deleted_at IS NULL`)).
		WithArgs(sqlmock.AnyArg(), "actor-1", "tenant-1", "member-b", "department-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE tenant_department_members SET is_primary=?,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND id=? AND version=? AND deleted_at IS NULL`)).
		WithArgs(true, sqlmock.AnyArg(), "actor-1", "tenant-1", "link-b", int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id,membership_id,is_primary,version FROM tenant_department_members WHERE tenant_id=? AND department_id=? AND membership_id=? AND deleted_at IS NOT NULL ORDER BY deleted_at DESC,id DESC LIMIT 1 FOR UPDATE`)).
		WithArgs("tenant-1", "department-1", "member-c").
		WillReturnRows(departmentMemberRows().AddRow("latest-c", "member-c", false, 9))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE tenant_department_members SET is_primary=?,deleted_at=NULL,deleted_by=NULL,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND id=? AND version=? AND deleted_at IS NOT NULL`)).
		WithArgs(false, sqlmock.AnyArg(), "actor-1", "tenant-1", "latest-c", int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()
	mock.ExpectClose()
	err := syncDepartmentMembers(t.Context(), tx, "tenant-1", "department-1", "actor-1", []DepartmentMemberAssignment{{MembershipID: "member-b", IsPrimary: true}, {MembershipID: "member-c"}}, time.Now())
	require.NoError(t, err)
}

func TestSyncDepartmentMembersRejectsStaleLinkWrite(t *testing.T) {
	tx, mock := departmentMemberSyncTx(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id,membership_id,is_primary,version FROM tenant_department_members WHERE tenant_id=? AND department_id=? AND deleted_at IS NULL ORDER BY membership_id,id LIMIT 1001 FOR UPDATE`)).
		WithArgs("tenant-1", "department-1").
		WillReturnRows(departmentMemberRows().AddRow("link-1", "member-1", false, 7))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE tenant_department_members SET is_primary=false,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND membership_id IN (?) AND department_id<>? AND is_primary=true AND deleted_at IS NULL`)).
		WithArgs(sqlmock.AnyArg(), "actor-1", "tenant-1", "member-1", "department-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE tenant_department_members SET is_primary=?,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND id=? AND version=? AND deleted_at IS NULL`)).
		WithArgs(true, sqlmock.AnyArg(), "actor-1", "tenant-1", "link-1", int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	mock.ExpectClose()
	err := syncDepartmentMembers(t.Context(), tx, "tenant-1", "department-1", "actor-1", []DepartmentMemberAssignment{{MembershipID: "member-1", IsPrimary: true}}, time.Now())
	require.ErrorIs(t, err, ErrConflict)
}

func TestSyncDepartmentMembersRejectsStaleHistoricalRestore(t *testing.T) {
	tx, mock := departmentMemberSyncTx(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id,membership_id,is_primary,version FROM tenant_department_members WHERE tenant_id=? AND department_id=? AND deleted_at IS NULL ORDER BY membership_id,id LIMIT 1001 FOR UPDATE`)).
		WithArgs("tenant-1", "department-1").WillReturnRows(departmentMemberRows())
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id,membership_id,is_primary,version FROM tenant_department_members WHERE tenant_id=? AND department_id=? AND membership_id=? AND deleted_at IS NOT NULL ORDER BY deleted_at DESC,id DESC LIMIT 1 FOR UPDATE`)).
		WithArgs("tenant-1", "department-1", "member-1").
		WillReturnRows(departmentMemberRows().AddRow("history-1", "member-1", false, 5))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE tenant_department_members SET is_primary=?,deleted_at=NULL,deleted_by=NULL,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND id=? AND version=? AND deleted_at IS NOT NULL`)).
		WithArgs(false, sqlmock.AnyArg(), "actor-1", "tenant-1", "history-1", int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	mock.ExpectClose()
	err := syncDepartmentMembers(t.Context(), tx, "tenant-1", "department-1", "actor-1", []DepartmentMemberAssignment{{MembershipID: "member-1"}}, time.Now())
	require.ErrorIs(t, err, ErrConflict)
}

func TestSyncDepartmentMembersBoundsCurrentSet(t *testing.T) {
	tx, mock := departmentMemberSyncTx(t)
	rows := departmentMemberRows()
	for range maxDepartmentAssignments + 1 {
		rows.AddRow("link", "member", false, 1)
	}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id,membership_id,is_primary,version FROM tenant_department_members WHERE tenant_id=? AND department_id=? AND deleted_at IS NULL ORDER BY membership_id,id LIMIT 1001 FOR UPDATE`)).
		WithArgs("tenant-1", "department-1").WillReturnRows(rows)
	mock.ExpectRollback()
	mock.ExpectClose()
	err := syncDepartmentMembers(t.Context(), tx, "tenant-1", "department-1", "actor-1", nil, time.Now())
	require.ErrorIs(t, err, ErrConflict)
}
