package tenant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/testutil"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestDepartmentLockPreservesInfrastructureAndLeaseLossErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		locker     testutil.LockStub
		want       error
		wantCalled bool
	}{
		{name: "acquisition failure", locker: testutil.LockStub{AcquireErr: context.DeadlineExceeded}, want: context.DeadlineExceeded},
		{name: "lease lost on release", locker: testutil.LockStub{ReleaseErr: cache.ErrLockLost}, want: cache.ErrLockLost, wantCalled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := &DepartmentService{locker: test.locker, cfg: config.Config{DistributedLock: config.DistributedLock{TTL: time.Second, RetryDelay: time.Millisecond}}}
			called := false
			err := service.withDepartmentLock(t.Context(), "tenant-1", "root-1", func(context.Context) error {
				called = true
				return nil
			})
			require.ErrorIs(t, err, test.want)
			require.NotErrorIs(t, err, ErrConflict)
			require.Equal(t, test.wantCalled, called)
		})
	}
}

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

func TestDepartmentCreateReturnsCommittedRowWhenActorPresentationFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenants WHERE id=\? AND status='active' AND deleted_at IS NULL`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`INSERT INTO tenant_departments`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM tenant_departments td WHERE td.tenant_id=\? AND td.id=\? AND td.deleted_at IS NULL`).WithArgs("tenant-a", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow(departmentActionID, "tenant-a", nil, "engineering", "Engineering", 0, now, "admin", now, "admin", 1))
	mock.ExpectCommit()
	service := &DepartmentService{db: db, tx: database.NewTransactor(db), users: mutationOwnerResolver{actorErr: errors.New("identity unavailable")}}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeSystem, TenantID: "tenant-a", MembershipID: "membership-a"})
	record, err := service.Create(ctx, DepartmentInput{Code: "engineering", Name: "Engineering"})
	require.NoError(t, err)
	require.Equal(t, "Engineering", record.Name)
	require.Equal(t, "admin", record.CreatedByName)
	require.Equal(t, int64(1), record.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDepartmentCreateRollsBackWhenResponseReadFails(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenants WHERE id=\? AND status='active' AND deleted_at IS NULL`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`INSERT INTO tenant_departments`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM tenant_departments td WHERE td.tenant_id=\? AND td.id=\? AND td.deleted_at IS NULL`).WithArgs("tenant-a", sqlmock.AnyArg()).WillReturnError(errors.New("read unavailable"))
	mock.ExpectRollback()
	service := &DepartmentService{db: db, tx: database.NewTransactor(db)}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeSystem, TenantID: "tenant-a", MembershipID: "membership-a"})
	_, err = service.Create(ctx, DepartmentInput{Code: "engineering", Name: "Engineering"})
	require.ErrorContains(t, err, "read unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDepartmentUpdateDoesNotRequireActorPresentationService(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	now := time.Now()
	columns := []string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}
	mock.ExpectQuery(`SELECT .* FROM tenant_departments td WHERE td.tenant_id=\? AND td.id=\? AND td.deleted_at IS NULL`).WithArgs("tenant-a", departmentActionID).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(departmentActionID, "tenant-a", nil, "engineering", "Engineering", 0, now, "admin", now, "admin", 2))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenants WHERE id=\? AND status='active' AND deleted_at IS NULL`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`UPDATE tenant_departments AS td SET parent_id=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM tenant_departments td WHERE td.tenant_id=\? AND td.id=\? AND td.deleted_at IS NULL`).WithArgs("tenant-a", departmentActionID).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(departmentActionID, "tenant-a", nil, "engineering", "Updated", 0, now, "admin", now, "admin", 3))
	mock.ExpectCommit()
	service := &DepartmentService{db: db, tx: database.NewTransactor(db), users: mutationOwnerResolver{actorErr: errors.New("identity unavailable")}}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeSystem, TenantID: "tenant-a", MembershipID: "membership-a"})
	record, err := service.Update(ctx, DepartmentUpdate{ID: departmentActionID, Name: "Updated", Version: 2})
	require.NoError(t, err)
	require.Equal(t, "Updated", record.Name)
	require.Equal(t, "admin", record.UpdatedByName)
	require.Equal(t, int64(3), record.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDepartmentDeleteDoesNotRequireActorPresentationService(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM tenant_departments td WHERE td.tenant_id=\? AND td.id=\? AND td.deleted_at IS NULL`).WithArgs("tenant-a", departmentActionID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow(departmentActionID, "tenant-a", nil, "engineering", "Engineering", 0, now, "admin", now, "admin", 2))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenants WHERE id=\? AND status='active' AND deleted_at IS NULL`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT \(SELECT count\(\*\) FROM tenant_departments`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(`UPDATE tenant_departments AS td SET deleted_at=`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	service := &DepartmentService{db: db, tx: database.NewTransactor(db), users: mutationOwnerResolver{actorErr: errors.New("identity unavailable")}}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeSystem, TenantID: "tenant-a", MembershipID: "membership-a"})
	require.NoError(t, service.Delete(ctx, departmentActionID, 2))
	require.NoError(t, mock.ExpectationsWereMet())
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
	require.ErrorIs(t, service.SetMembers(ctx, "department-a", 1, []DepartmentMemberAssignment{{MembershipID: overlongID}}), ErrInvalid)
	require.ErrorIs(t, service.SetMembers(ctx, "department-a", 0, nil), ErrInvalid)
	_, err = service.Members(ctx, overlongID)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestClaimDepartmentAssignmentVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		stored      int64
		updatedRows int64
		wantStale   bool
	}{
		{name: "current version is claimed", stored: 3, updatedRows: 1},
		{name: "stale editor cannot write links", stored: 4, wantStale: true},
		{name: "concurrent update loses claim", stored: 3, updatedRows: 0, wantStale: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			db := sqlx.NewDb(raw, "sqlmock")
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT td.name,td.version FROM tenant_departments td WHERE td.tenant_id=\? AND td.id=\? AND td.deleted_at IS NULL AND \(td.created_by = \?\) FOR UPDATE`).
				WithArgs("tenant-a", "department-a", "admin-a").
				WillReturnRows(sqlmock.NewRows([]string{"name", "version"}).AddRow("研发部", test.stored))
			if test.stored == 3 {
				mock.ExpectExec(`UPDATE tenant_departments AS td SET updated_at=\?,updated_by=\?,version=version\+1 WHERE td.tenant_id=\? AND td.id=\? AND td.version=\? AND td.deleted_at IS NULL AND \(td.created_by = \?\)`).
					WithArgs(sqlmock.AnyArg(), "admin-a", "tenant-a", "department-a", int64(3), "admin-a").
					WillReturnResult(sqlmock.NewResult(0, test.updatedRows))
			}
			mock.ExpectRollback()
			tx, err := db.BeginTxx(t.Context(), nil)
			require.NoError(t, err)
			name, err := claimDepartmentAssignmentVersion(t.Context(), tx, "tenant-a", "department-a", 3, "admin-a", datapermission.SQLPredicate{Clause: "(td.created_by = ?)", Args: []any{"admin-a"}})
			if test.wantStale {
				require.ErrorIs(t, err, ErrStaleVersion)
			} else {
				require.NoError(t, err)
				require.Equal(t, "研发部", name)
			}
			require.NoError(t, tx.Rollback())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestDepartmentMembersAppliesTenantAndScopedDepartmentBeforeListing(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	now := time.Now()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT `+departmentColumns+` FROM tenant_departments td WHERE td.tenant_id=? AND td.id=? AND td.deleted_at IS NULL AND (1 = 1)`)).
		WithArgs("tenant-a", "department-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow("department-a", "tenant-a", nil, "engineering", "Engineering", 0, now, "user-a", now, "user-a", 1))
	mock.ExpectQuery(`SELECT m.id AS membership_id,m.user_id,m.username,m.display_name,m.status,m.joined_at,dm.is_primary FROM tenant_department_members dm JOIN tenant_memberships m`).
		WithArgs("tenant-a", "department-a").
		WillReturnRows(sqlmock.NewRows([]string{"membership_id", "user_id", "username", "display_name", "status", "joined_at", "is_primary"}).
			AddRow("membership-a", "user-a", "alice", "Alice", "active", now, true))
	service := &DepartmentService{db: db}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-a", MembershipID: "membership-system"})

	members, err := service.Members(ctx, "department-a")
	require.NoError(t, err)
	require.Equal(t, "membership-a", members[0].MembershipID)
	require.Equal(t, "Alice", members[0].DisplayName)
	require.True(t, members[0].IsPrimary)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDepartmentServiceTreeBoundsDatabaseResult(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + departmentColumns + ` FROM tenant_departments td WHERE td.tenant_id=? AND td.deleted_at IS NULL AND (1 = 1) ORDER BY sort_order,id LIMIT 10001`)).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}))
	service := &DepartmentService{db: db}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "admin", Type: platformprincipal.TypeSystem, TenantID: "tenant-a", MembershipID: "member-a"})

	tree, err := service.Tree(ctx, "")
	require.NoError(t, err)
	require.Empty(t, tree)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDepartmentTreePresentsParentFromVisibleNodes(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	now := time.Now()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + departmentColumns + ` FROM tenant_departments td WHERE td.tenant_id=? AND td.deleted_at IS NULL AND (1 = 1) ORDER BY sort_order,id LIMIT 10001`)).
		WithArgs("tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow("parent-a", "tenant-a", nil, "parent", "Parent", 0, now, "system", now, "system", 1).
			AddRow("child-a", "tenant-a", "parent-a", "child", "Child", 1, now, "system", now, "system", 1))
	service := &DepartmentService{db: db}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "system", Type: platformprincipal.TypeSystem, TenantID: "tenant-a", MembershipID: "membership-system"})
	tree, err := service.Tree(ctx, "child")
	require.NoError(t, err)
	require.Len(t, tree, 1)
	require.Len(t, tree[0].Children, 1)
	require.Equal(t, "Parent", tree[0].Children[0].ParentName)
	encoded, err := json.Marshal(tree)
	require.NoError(t, err)
	var response []struct {
		ParentID json.RawMessage `json:"parent_id"`
		Children []struct {
			ParentID json.RawMessage `json:"parent_id"`
			Children []any           `json:"children"`
		} `json:"children"`
	}
	require.NoError(t, json.Unmarshal(encoded, &response))
	require.JSONEq(t, `null`, string(response[0].ParentID))
	require.JSONEq(t, `"parent-a"`, string(response[0].Children[0].ParentID))
	require.NotNil(t, response[0].Children[0].Children)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDepartmentTreeAppliesTenantAndDataPermissionBeforeConstruction(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT `+departmentColumns+` FROM tenant_departments td WHERE td.tenant_id=? AND td.deleted_at IS NULL AND (td.created_by = ?) ORDER BY sort_order,id LIMIT 10001`)).
		WithArgs("tenant-a", "user-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}))
	service := &DepartmentService{db: db}

	scope := datapermission.SQLPredicate{Clause: "(td.created_by = ?)", Args: []any{"user-a"}}
	records, err := service.listTreeRecords(t.Context(), "tenant-a", scope)
	require.NoError(t, err)
	require.Empty(t, records)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDepartmentGetAppliesTenantAndDataPermissionInOneQuery(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	now := time.Now()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT `+departmentColumns+` FROM tenant_departments td WHERE td.tenant_id=? AND td.id=? AND td.deleted_at IS NULL AND (td.created_by = ?)`)).
		WithArgs("tenant-a", "department-a", "user-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow("department-a", "tenant-a", nil, "engineering", "Engineering", 0, now, "user-a", now, "user-a", 1))
	service := &DepartmentService{db: db}
	record, err := service.get(t.Context(), "tenant-a", "department-a", datapermission.SQLPredicate{Clause: "(td.created_by = ?)", Args: []any{"user-a"}})
	require.NoError(t, err)
	require.Equal(t, "department-a", record.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDepartmentGetResolvesParentNameOnlyWithinTenantAndDataScope(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	now := time.Now()
	scope := datapermission.SQLPredicate{Clause: "(td.created_by = ?)", Args: []any{"user-a"}}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT `+departmentColumns+` FROM tenant_departments td WHERE td.tenant_id=? AND td.id=? AND td.deleted_at IS NULL AND (td.created_by = ?)`)).
		WithArgs("tenant-a", "child-a", "user-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow("child-a", "tenant-a", "parent-a", "child", "Child", 0, now, "user-a", now, "user-a", 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT td.name FROM tenant_departments td WHERE td.tenant_id=? AND td.id=? AND td.deleted_at IS NULL AND (td.created_by = ?)`)).
		WithArgs("tenant-a", "parent-a", "user-a").
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Parent"))
	service := &DepartmentService{db: db}
	record, err := service.get(t.Context(), "tenant-a", "child-a", scope)
	require.NoError(t, err)
	require.Equal(t, "Parent", record.ParentName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDepartmentGetDoesNotExposeParentOutsideDataScope(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	now := time.Now()
	scope := datapermission.SQLPredicate{Clause: "(td.created_by = ?)", Args: []any{"user-a"}}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT `+departmentColumns+` FROM tenant_departments td WHERE td.tenant_id=? AND td.id=? AND td.deleted_at IS NULL AND (td.created_by = ?)`)).
		WithArgs("tenant-a", "child-a", "user-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow("child-a", "tenant-a", "parent-a", "child", "Child", 0, now, "user-a", now, "user-a", 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT td.name FROM tenant_departments td WHERE td.tenant_id=? AND td.id=? AND td.deleted_at IS NULL AND (td.created_by = ?)`)).
		WithArgs("tenant-a", "parent-a", "user-a").
		WillReturnError(sql.ErrNoRows)
	service := &DepartmentService{db: db}
	record, err := service.get(t.Context(), "tenant-a", "child-a", scope)
	require.NoError(t, err)
	require.Empty(t, record.ParentName)
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
