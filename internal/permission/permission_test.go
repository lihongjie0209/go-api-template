package permission

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	platformtree "github.com/lihongjie0209/microservice-platform-go/tree"
	"github.com/stretchr/testify/require"
)

func TestPermissionTreeJSONIncludesNullableParentAndChildren(t *testing.T) {
	t.Parallel()
	rootID := "root"
	nodes := mapTree([]*platformtree.Node[Record]{{
		Value: Record{ID: rootID},
		Children: []*platformtree.Node[Record]{{
			Value: Record{ID: "child", ParentID: &rootID},
		}},
	}})
	encoded, err := json.Marshal(nodes)
	require.NoError(t, err)
	var response []struct {
		ParentID json.RawMessage `json:"parent_id"`
		Children []struct {
			ParentID json.RawMessage `json:"parent_id"`
			Children []any           `json:"children"`
		} `json:"children"`
	}
	require.NoError(t, json.Unmarshal(encoded, &response))
	require.Len(t, response, 1)
	require.JSONEq(t, `null`, string(response[0].ParentID))
	require.Len(t, response[0].Children, 1)
	require.JSONEq(t, `"root"`, string(response[0].Children[0].ParentID))
	require.NotNil(t, response[0].Children[0].Children)
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input Input
		want  error
	}{
		{name: "permission", input: Input{Key: "platform.user.read", Name: "查询用户", NodeType: "permission", Resource: "platform.user", Action: "read", Status: "active"}},
		{name: "group cannot authorize", input: Input{Key: "group.user", Name: "用户", NodeType: "group", Resource: "platform.user", Status: "active"}, want: ErrInvalid},
		{name: "permission needs action", input: Input{Key: "platform.user", Name: "用户", NodeType: "permission", Resource: "platform.user", Status: "active"}, want: ErrInvalid},
		{name: "invalid key", input: Input{Key: "用户", Name: "用户", NodeType: "group", Status: "active"}, want: ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validate(tt.input); !errors.Is(err, tt.want) {
				t.Fatalf("validate()=%v want=%v", err, tt.want)
			}
		})
	}
}

func TestServiceValidationRequiresRegisteredResourceAction(t *testing.T) {
	t.Parallel()
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	service := &Service{resources: resources}
	require.NoError(t, service.validateInput(t.Context(), Input{Key: "platform.users.read", Name: "查询用户", NodeType: "permission", Resource: "identity.user", Action: "read", Status: "active"}))
	require.ErrorIs(t, service.validateInput(t.Context(), Input{Key: "platform.unknown.read", Name: "未知资源", NodeType: "permission", Resource: "unknown.resource", Action: "read", Status: "active"}), ErrInvalid)
	require.ErrorIs(t, service.validateInput(t.Context(), Input{Key: "platform.users.unknown", Name: "未知操作", NodeType: "permission", Resource: "identity.user", Action: "unknown", Status: "active"}), ErrInvalid)
}

func TestServiceValidationPropagatesResourceLookupFailure(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	service := &Service{resources: resources, repository: NewRepository(sqlx.NewDb(raw, "sqlmock"))}
	mock.ExpectQuery(`SELECT count\(\*\) FROM authorization_resources r JOIN authorization_resource_actions a`).
		WithArgs("external.resource", "read").
		WillReturnError(context.DeadlineExceeded)
	err = service.validateInput(t.Context(), Input{Key: "platform.external.read", Name: "Read external", NodeType: "permission", Resource: "external.resource", Action: "read", Status: "active"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, ErrInvalid)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPermissionUpdatePropagatesNewActionScopeLookupFailure(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlx.NewDb(raw, "sqlmock")
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	service := &Service{resources: resources, transactor: database.NewTransactor(db)}
	const id = "08896c3a-4c82-473e-90c8-b59f60446139"
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM permissions WHERE id=\? AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows(strings.Split(columns, ", ")).
			AddRow(id, nil, "platform.user.read", "Read user", "permission", "identity.user", "read", "", 0, "active", false, now, "actor", now, "actor", 1))
	mock.ExpectQuery(`SELECT r.scope FROM authorization_resources r JOIN authorization_resource_actions a`).
		WithArgs("identity.user", "read").
		WillReturnRows(sqlmock.NewRows([]string{"scope"}).AddRow("platform"))
	mock.ExpectQuery(`SELECT r.scope FROM authorization_resources r JOIN authorization_resource_actions a`).
		WithArgs("identity.user", "update").
		WillReturnError(context.DeadlineExceeded)
	mock.ExpectRollback()
	ctx := platformprincipal.SystemContext(t.Context(), "actor")
	_, err = service.Update(ctx, id, 1, Input{Key: "platform.user.update", Name: "Update user", NodeType: "permission", Resource: "identity.user", Action: "update", Status: "active"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, ErrInvalid)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSetActionsRejectsDuplicateAndUnboundedInputBeforeDatabase(t *testing.T) {
	t.Parallel()
	service := &Service{}
	permissionID := "08896c3a-4c82-473e-90c8-b59f60446139"
	actionID := "18f4762f-f4f9-47d5-bd7a-6f07af436f58"
	_, err := service.SetActions(t.Context(), permissionID, 1, []string{actionID, actionID})
	require.ErrorIs(t, err, ErrInvalid)
	_, err = service.SetActions(t.Context(), permissionID, 1, nil)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = service.SetActions(t.Context(), permissionID, 1, []string{"not-uuid"})
	require.ErrorIs(t, err, ErrInvalid)
}

func TestActionEditTargetReturnsOnlyEditablePermissionAndLinkedActions(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service := &Service{repository: NewRepository(sqlx.NewDb(db, "sqlmock"))}
	permissionID := "08896c3a-4c82-473e-90c8-b59f60446139"
	actionID := "18f4762f-f4f9-47d5-bd7a-6f07af436f58"
	now := time.Now()
	mock.ExpectQuery(`SELECT p\.id.*FROM permissions p LEFT JOIN permissions parent`).
		WithArgs(permissionID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "parent_id", "parent_name", "permission_key", "name", "node_type", "resource", "action", "description", "sort_order", "status", "is_system", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow(permissionID, nil, "", "platform.user.read", "Read users", "permission", "identity.user", "read", "private description", 0, "active", false, now, "creator", now, "updater", 4))
	mock.ExpectQuery(`SELECT a\.id,r\.resource_key,a\.action_key`).
		WithArgs(permissionID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "resource_key", "action_key", "resource_name", "action_name", "scope"}).
			AddRow(actionID, "identity.user", "read", "Users", "Read", "platform"))

	target, err := service.ActionEditTarget(t.Context(), permissionID)
	require.NoError(t, err)
	require.Equal(t, ActionEditTarget{
		ID:            permissionID,
		PermissionKey: "platform.user.read",
		Name:          "Read users",
		Resource:      "identity.user",
		Action:        "read",
		Version:       4,
		Actions:       []ActionReference{{ID: actionID, ResourceKey: "identity.user", ActionKey: "read", ResourceName: "Users", ActionName: "Read", Scope: "platform"}},
	}, target)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestActionEditTargetRejectsInvalidAndNoneditableNodes(t *testing.T) {
	t.Parallel()
	service := &Service{}
	_, err := service.ActionEditTarget(t.Context(), "not-a-uuid")
	require.ErrorIs(t, err, ErrInvalid)

	for _, tc := range []struct {
		name     string
		nodeType string
		isSystem bool
	}{
		{name: "group", nodeType: "group"},
		{name: "system permission", nodeType: "permission", isSystem: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			permissionID := "08896c3a-4c82-473e-90c8-b59f60446139"
			now := time.Now()
			mock.ExpectQuery(`SELECT p\.id.*FROM permissions p LEFT JOIN permissions parent`).
				WithArgs(permissionID).
				WillReturnRows(sqlmock.NewRows([]string{"id", "parent_id", "parent_name", "permission_key", "name", "node_type", "resource", "action", "description", "sort_order", "status", "is_system", "created_at", "created_by", "updated_at", "updated_by", "version"}).
					AddRow(permissionID, nil, "", "platform.user.read", "Read users", tc.nodeType, "identity.user", "read", "", 0, "active", tc.isSystem, now, "creator", now, "updater", 4))
			local := &Service{repository: NewRepository(sqlx.NewDb(db, "sqlmock"))}
			_, err = local.ActionEditTarget(t.Context(), permissionID)
			require.ErrorIs(t, err, ErrConflict)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSetActionsRejectsCrossScopeLink(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	service := &Service{transactor: database.NewTransactor(sqlxDB)}
	permissionID := "08896c3a-4c82-473e-90c8-b59f60446139"
	actionID := "18f4762f-f4f9-47d5-bd7a-6f07af436f58"
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM permissions WHERE id=\?`).WithArgs(permissionID).WillReturnRows(
		sqlmock.NewRows(strings.Split(columns, ", ")).AddRow(permissionID, nil, "platform.user.read", "Read users", "permission", "identity.user", "read", "", 0, "active", false, now, "actor", now, "actor", 1),
	)
	mock.ExpectQuery(`SELECT r.scope FROM authorization_resources`).WithArgs("identity.user", "read").WillReturnRows(sqlmock.NewRows([]string{"scope"}).AddRow("platform"))
	mock.ExpectQuery(`SELECT a.id,r.scope,r.resource_key,a.action_key FROM authorization_resource_actions`).WithArgs(actionID).WillReturnRows(sqlmock.NewRows([]string{"id", "scope", "resource_key", "action_key"}).AddRow(actionID, "tenant", "identity.user", "read"))
	mock.ExpectRollback()
	_, err = service.SetActions(platformprincipal.SystemContext(t.Context(), "actor"), permissionID, 1, []string{actionID})
	require.ErrorIs(t, err, ErrInvalid)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSetActionsRequiresPrimaryAction(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service := &Service{transactor: database.NewTransactor(sqlx.NewDb(db, "sqlmock"))}
	permissionID := "08896c3a-4c82-473e-90c8-b59f60446139"
	actionID := "18f4762f-f4f9-47d5-bd7a-6f07af436f58"
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM permissions WHERE id=\?`).WithArgs(permissionID).WillReturnRows(
		sqlmock.NewRows(strings.Split(columns, ", ")).AddRow(permissionID, nil, "platform.user.read", "Read users", "permission", "identity.user", "read", "", 0, "active", false, now, "actor", now, "actor", 1),
	)
	mock.ExpectQuery(`SELECT r.scope FROM authorization_resources`).WithArgs("identity.user", "read").WillReturnRows(sqlmock.NewRows([]string{"scope"}).AddRow("platform"))
	mock.ExpectQuery(`SELECT a.id,r.scope,r.resource_key,a.action_key FROM authorization_resource_actions`).WithArgs(actionID).WillReturnRows(
		sqlmock.NewRows([]string{"id", "scope", "resource_key", "action_key"}).AddRow(actionID, "platform", "identity.user", "update"),
	)
	mock.ExpectRollback()
	_, err = service.SetActions(platformprincipal.SystemContext(t.Context(), "actor"), permissionID, 1, []string{actionID})
	require.ErrorIs(t, err, ErrInvalid)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCapabilityPermissionSeedIDGoldenMapping(t *testing.T) {
	t.Parallel()
	id, err := SeedID("authorization.capability.evaluate")
	require.NoError(t, err)
	require.Equal(t, "113ab186-bba8-5d22-b37f-f1f65bb4b211", id)
}

type actorResolverStub struct {
	calls int
}

func (s *actorResolverStub) ResolveUserIDs(_ context.Context, _ []string) (map[string]string, error) {
	s.calls++
	return map[string]string{"user-1": "Alice"}, nil
}

func TestPermissionPresentationBatchesActorsAndUsesPlatformTimezone(t *testing.T) {
	t.Parallel()
	resolver := &actorResolverStub{}
	service := &Service{actors: resolver}
	instant := time.Date(2026, time.September, 16, 1, 2, 3, 0, time.UTC)
	records := []Record{{CreatedBy: "user-1", UpdatedBy: "system-1", CreatedAt: instant, UpdatedAt: instant}, {CreatedBy: "user-1", UpdatedBy: "user-1", CreatedAt: instant, UpdatedAt: instant}}

	require.NoError(t, service.present(t.Context(), records))
	require.Equal(t, 1, resolver.calls)
	require.Equal(t, "Alice", records[0].CreatedByName)
	require.Equal(t, "system-1", records[0].UpdatedByName)
	require.Equal(t, "2026-09-16T09:02:03+08:00", records[0].CreatedAt.Format(time.RFC3339))
}

func TestFilterRecordsRetainsAncestors(t *testing.T) {
	t.Parallel()
	root, group := "root", "group"
	records := []Record{
		{ID: root, Key: "platform", Name: "平台", NodeType: "group", Status: "active"},
		{ID: group, ParentID: &root, Key: "platform.users", Name: "用户", NodeType: "group", Status: "active"},
		{ID: "read", ParentID: &group, Key: "platform.users.read", Name: "查询用户", NodeType: "permission", Resource: "users", Action: "read", Status: "active"},
		{ID: "write", ParentID: &group, Key: "platform.users.write", Name: "修改用户", NodeType: "permission", Resource: "users", Action: "write", Status: "disabled"},
	}

	filtered := filterRecords(records, TreeInput{Keyword: "查询", NodeTypes: []string{"permission"}, Statuses: []string{"active"}})
	require.Equal(t, []Record{records[0], records[1], records[2]}, filtered)
}

func TestAttachParentNamesUsesOnlyReturnedRecords(t *testing.T) {
	t.Parallel()
	root, hidden := "root", "hidden"
	records := []Record{
		{ID: root, Name: "系统"},
		{ID: "child", ParentID: &root, Name: "用户"},
		{ID: "orphan", ParentID: &hidden, Name: "孤立节点"},
	}
	attachParentNames(records)
	require.Equal(t, "", records[0].ParentName)
	require.Equal(t, "系统", records[1].ParentName)
	require.Equal(t, "", records[2].ParentName)
}

func TestRepositoryGetResolvesOnlyActiveParentName(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository := NewRepository(sqlx.NewDb(db, "sqlmock"))
	now := time.Now()
	rows := sqlmock.NewRows([]string{"id", "parent_id", "parent_name", "permission_key", "name", "node_type", "resource", "action", "description", "sort_order", "status", "is_system", "created_at", "created_by", "updated_at", "updated_by", "version"}).
		AddRow("child", "root", "系统", "platform.child", "子权限", "group", "", "", "", 0, "active", false, now, "actor", now, "actor", 1)
	mock.ExpectQuery(`SELECT p\.id.*LEFT JOIN permissions parent ON parent\.id=p\.parent_id AND parent\.deleted_at IS NULL`).
		WithArgs("child").WillReturnRows(rows)
	record, err := repository.Get(t.Context(), "child")
	require.NoError(t, err)
	require.Equal(t, "系统", record.ParentName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFilterRecordsWithNoMatchReturnsEmptySlice(t *testing.T) {
	t.Parallel()
	filtered := filterRecords([]Record{{ID: "root", Key: "platform", NodeType: "group", Status: "active"}}, TreeInput{Keyword: "missing"})
	require.NotNil(t, filtered)
	require.Empty(t, filtered)
}

func TestPermissionNodesCannotOwnChildren(t *testing.T) {
	t.Parallel()
	parent := "permission-parent"
	require.False(t, hasValidParentTypes([]Record{
		{ID: parent, NodeType: "permission"},
		{ID: "child", ParentID: &parent, NodeType: "permission"},
	}))
}

func TestValidateTreeTxChecksProposedNodeType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		hasChild  bool
		nodeType  string
		wantError error
	}{
		{name: "group with child cannot become permission", hasChild: true, nodeType: "permission", wantError: ErrInvalid},
		{name: "leaf group can become permission", nodeType: "permission"},
		{name: "group with child can remain group", hasChild: true, nodeType: "group"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			sqlxDB := sqlx.NewDb(db, "sqlmock")
			mock.ExpectBegin()
			tx, err := sqlxDB.BeginTxx(t.Context(), nil)
			require.NoError(t, err)
			now := time.Now()
			rows := sqlmock.NewRows(strings.Split(columns, ", ")).AddRow("group", nil, "platform.group", "Group", "group", "", "", "", 0, "active", false, now, "actor", now, "actor", 1)
			if tt.hasChild {
				rows.AddRow("child", "group", "platform.child", "Child", "permission", "identity.user", "read", "", 0, "active", false, now, "actor", now, "actor", 1)
			}
			mock.ExpectQuery(`SELECT .* FROM permissions WHERE deleted_at IS NULL ORDER BY sort_order,id LIMIT 10001`).WillReturnRows(rows)
			mock.ExpectRollback()
			err = (&Service{}).validateTreeTx(t.Context(), tx, "group", nil, tt.nodeType)
			if tt.wantError != nil {
				require.ErrorIs(t, err, tt.wantError)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, tx.Rollback())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestNodeMutationsRejectStaleVersionBeforeWriting(t *testing.T) {
	t.Parallel()
	const id = "08896c3a-4c82-473e-90c8-b59f60446139"
	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			service := &Service{repository: NewRepository(sqlx.NewDb(raw, "sqlmock")), transactor: database.NewTransactor(sqlx.NewDb(raw, "sqlmock"))}
			now := time.Now()
			if operation == "delete" {
				mock.ExpectQuery(`SELECT p\.id.*FROM permissions p LEFT JOIN permissions parent`).WithArgs(id).
					WillReturnRows(sqlmock.NewRows([]string{"id", "parent_id", "parent_name", "permission_key", "name", "node_type", "resource", "action", "description", "sort_order", "status", "is_system", "created_at", "created_by", "updated_at", "updated_by", "version"}).
						AddRow(id, nil, "", "platform.group", "Group", "group", "", "", "", 0, "active", false, now, "actor", now, "actor", 2))
			}
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT .* FROM permissions WHERE id=\?`).WithArgs(id).
				WillReturnRows(sqlmock.NewRows(strings.Split(columns, ", ")).
					AddRow(id, nil, "platform.group", "Group", "group", "", "", "", 0, "active", false, now, "actor", now, "actor", 2))
			mock.ExpectRollback()
			ctx := platformprincipal.SystemContext(t.Context(), "actor")
			if operation == "update" {
				_, err = service.Update(ctx, id, 1, Input{Key: "platform.group", Name: "Group", NodeType: "group", Status: "active"})
			} else {
				err = service.Delete(ctx, id, 1)
			}
			require.ErrorIs(t, err, ErrStaleVersion)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestTreeFiltersAreBoundedAndEnumerated(t *testing.T) {
	t.Parallel()
	require.False(t, validFilters(TreeInput{NodeTypes: []string{"unknown"}}))
	require.False(t, validFilters(TreeInput{Statuses: []string{"deleted"}}))
}

func TestValidateRejectsOversizedFieldsAndSortOrder(t *testing.T) {
	t.Parallel()
	base := Input{Key: "platform.users.read", Name: "read users", NodeType: "permission", Resource: "users", Action: "read", Status: "active"}
	tests := []Input{
		func() Input { value := base; value.Name = string(make([]byte, maxNameLength+1)); return value }(),
		func() Input { value := base; value.Resource = string(make([]byte, maxResourceLength+1)); return value }(),
		func() Input { value := base; value.SortOrder = maxSortOrder + 1; return value }(),
	}
	for _, input := range tests {
		require.ErrorIs(t, validate(input), ErrInvalid)
	}
}

func TestMutationRollsBackWhenTransactionalAuditFails(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	recorder := &failingOperationRecorder{err: errors.New("outbox unavailable")}
	service := &Service{
		transactor: database.NewTransactor(sqlxDB),
		operations: recorder, security: securityRecorderStub{},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mock.ExpectBegin()
	mock.ExpectRollback()

	err = service.mutate(platformprincipal.SystemContext(t.Context(), "actor-1"), "permission.update", "permission-1", nil, func(*sqlx.Tx) error { return nil })
	require.ErrorContains(t, err, "outbox unavailable")
	require.Equal(t, 1, recorder.transactionalCalls)
	require.Equal(t, 1, recorder.failureCalls)
	require.NoError(t, mock.ExpectationsWereMet())
	_ = db.Close()
}

type failingOperationRecorder struct {
	err                error
	transactionalCalls int
	failureCalls       int
}

func (*failingOperationRecorder) Enabled() bool { return true }
func (r *failingOperationRecorder) Record(_ context.Context, entry operationlog.Entry) error {
	if !entry.Succeeded {
		r.failureCalls++
	}
	return nil
}
func (r *failingOperationRecorder) RecordTx(context.Context, *sqlx.Tx, operationlog.Entry) error {
	r.transactionalCalls++
	return r.err
}

type securityRecorderStub struct{}

func (securityRecorderStub) Enabled() bool                                   { return true }
func (securityRecorderStub) FailClosed() bool                                { return true }
func (securityRecorderStub) Record(context.Context, securitylog.Entry) error { return nil }
func (securityRecorderStub) RecordTx(context.Context, *sqlx.Tx, securitylog.Entry) error {
	return nil
}
