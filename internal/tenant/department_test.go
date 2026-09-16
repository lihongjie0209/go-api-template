package tenant

import (
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestValidateDepartmentMove(t *testing.T) {
	root, child := "root", "child"
	tests := []struct {
		name, id, parent string
		parents          map[string]*string
		wantErr          bool
	}{
		{name: "move to sibling", id: "child", parent: "sibling", parents: map[string]*string{"sibling": &root}, wantErr: false},
		{name: "move under descendant", id: "root", parent: "grandchild", parents: map[string]*string{"grandchild": &child, "child": &root}, wantErr: true},
		{name: "corrupt existing cycle fails closed", id: "other", parent: "a", parents: map[string]*string{"a": ptr("b"), "b": ptr("a")}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDepartmentMove(test.id, test.parent, test.parents)
			if test.wantErr {
				require.ErrorIs(t, err, ErrInvalid)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestFilterDepartmentsRetainsAncestorsAndSearchesCode(t *testing.T) {
	root, group := "root", "group"
	records := []Department{
		{ID: root, Code: "company", Name: "公司"},
		{ID: group, ParentID: &root, Code: "engineering", Name: "研发"},
		{ID: "backend", ParentID: &group, Code: "backend_platform", Name: "后端"},
		{ID: "sales", ParentID: &root, Code: "sales", Name: "销售"},
	}
	require.Equal(t, records[:3], filterDepartments(records, "BACKEND"))
	missing := filterDepartments(records, "missing")
	require.NotNil(t, missing)
	require.Empty(t, missing)
}
func ptr(value string) *string { return &value }

func TestDepartmentPresentationUsesOneIdentityBatchAndPlatformTimezone(t *testing.T) {
	t.Parallel()
	client := &identityClientStub{}
	service := &DepartmentService{users: &grpcUserResolver{client: client}}
	instant := time.Date(2026, time.September, 16, 1, 2, 3, 0, time.UTC)
	records := []Department{
		{CreatedBy: "user-1", UpdatedBy: "system-1", CreatedAt: instant, UpdatedAt: instant},
		{CreatedBy: "user-1", UpdatedBy: "user-1", CreatedAt: instant, UpdatedAt: instant},
	}

	require.NoError(t, service.presentDepartments(t.Context(), records))
	require.Equal(t, "Alice", records[0].CreatedByName)
	require.Equal(t, "system-1", records[0].UpdatedByName)
	require.Equal(t, "2026-09-16T09:02:03+08:00", records[0].CreatedAt.Format(time.RFC3339))
	require.Len(t, client.batchRequest.GetUserIds(), 2)
}

func TestDepartmentServiceRejectsUnboundedInputBeforeDatabase(t *testing.T) {
	t.Parallel()
	service := &DepartmentService{}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeUser, TenantID: "tenant-a", MembershipID: "member-a"})
	overlongID := string(make([]byte, maxTenantIDLength+1))
	overlongName := string(make([]byte, maxDepartmentNameLength+1))

	_, err := service.Create(ctx, DepartmentInput{Code: "engineering", Name: overlongName})
	require.ErrorIs(t, err, ErrInvalid)
	_, err = service.Get(ctx, overlongID)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = service.Update(ctx, DepartmentUpdate{ID: "department-a", Name: "Engineering", SortOrder: maxDepartmentSortOrder + 1, Version: 1})
	require.ErrorIs(t, err, ErrInvalid)
	require.ErrorIs(t, service.Delete(ctx, overlongID, 1), ErrInvalid)
	require.ErrorIs(t, service.SetMembers(ctx, "department-a", []DepartmentMemberAssignment{{MembershipID: overlongID}}), ErrInvalid)
}

func TestDepartmentServiceTreeBoundsDatabaseResult(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + departmentColumns + ` FROM tenant_departments WHERE tenant_id=? AND deleted_at IS NULL ORDER BY sort_order,id LIMIT 10001`)).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}))
	service := &DepartmentService{db: db}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeUser, TenantID: "tenant-a", MembershipID: "member-a"})

	tree, err := service.Tree(ctx, "")
	require.NoError(t, err)
	require.Empty(t, tree)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDepartmentMutationRollsBackWhenTransactionalOperationLogFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	wantErr := errors.New("operation outbox unavailable")
	operations := &transactionalOperationRecorderStub{txErr: wantErr}
	service := &DepartmentService{tx: database.NewTransactor(db), operations: operations}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE tenant_departments SET name`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeUser, TenantID: "tenant-a", MembershipID: "member-a"})

	err = service.mutate(ctx, "tenant.department.update", "department-a", map[string]any{"name": "Engineering"}, nil, func(tx *sqlx.Tx) error {
		_, execErr := tx.ExecContext(ctx, `UPDATE tenant_departments SET name='Engineering'`)
		return execErr
	})
	require.ErrorIs(t, err, wantErr)
	require.Len(t, operations.txEntries, 1)
	require.Len(t, operations.standalone, 1)
	require.False(t, operations.standalone[0].Succeeded)
	require.NoError(t, mock.ExpectationsWereMet())
}
