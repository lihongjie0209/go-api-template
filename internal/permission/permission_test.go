package permission

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

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

func TestServiceValidationRequiresRegisteredPBACResourceAction(t *testing.T) {
	t.Parallel()
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	require.NoError(t, err)
	service := &Service{resources: resources}
	require.NoError(t, service.validateInput(Input{Key: "platform.users.read", Name: "查询用户", NodeType: "permission", Resource: "identity.user", Action: "read", Status: "active"}))
	require.ErrorIs(t, service.validateInput(Input{Key: "platform.unknown.read", Name: "未知资源", NodeType: "permission", Resource: "unknown.resource", Action: "read", Status: "active"}), ErrInvalid)
	require.ErrorIs(t, service.validateInput(Input{Key: "platform.users.unknown", Name: "未知操作", NodeType: "permission", Resource: "identity.user", Action: "unknown", Status: "active"}), ErrInvalid)
}

func TestPermissionSeedIDGoldenMapping(t *testing.T) {
	t.Parallel()
	id, err := SeedID("platform.pbac-policy.manage")
	require.NoError(t, err)
	require.Equal(t, "a0d93b54-6f9d-5136-abb5-27897aef65ad", id)
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
