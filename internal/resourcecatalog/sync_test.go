package resourcecatalog

import (
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	"github.com/lihongjie0209/microservice-platform-go/stableid"
	"github.com/stretchr/testify/require"
)

const testBuildTime = "2026-10-13T08:00:00Z"

func testRegistry(t *testing.T) *resourceaction.Registry {
	t.Helper()
	registry, err := resourceaction.NewRegistry([]resourceaction.ResourceDefinition{{
		Key: "tenant.member", Name: "成员", Description: "租户成员", Scope: resourceaction.ResourceScopeTenant,
		Actions: []resourceaction.ActionDefinition{{Key: "read", Name: "查看", Description: "查看成员"}},
	}})
	require.NoError(t, err)
	return registry
}

func testSyncer(t *testing.T, registry *resourceaction.Registry) (*Syncer, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
	})
	sqlxDB := sqlx.NewDb(db, "pgx")
	return New(sqlxDB, database.NewTransactor(sqlxDB), registry), mock
}

func expectedIDs(t *testing.T) (string, string) {
	t.Helper()
	generator, err := stableid.New(Namespace)
	require.NoError(t, err)
	resourceID, err := generator.String("resource:tenant.member")
	require.NoError(t, err)
	actionID, err := generator.String("action:tenant.member:read")
	require.NoError(t, err)
	return resourceID, actionID
}

func TestStableCatalogIDs(t *testing.T) {
	t.Parallel()
	resourceID, actionID := expectedIDs(t)
	require.Equal(t, "6f555bcd-b3bb-5667-9807-240918cc48e1", resourceID)
	require.Equal(t, "9030bd35-3480-5e72-afd5-73f2b49bee04", actionID)
}

func TestBackfillLegacyPermissionLinksOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	sqlxDB := sqlx.NewDb(db, "pgx")
	generator, err := stableid.New(Namespace)
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT source FROM authorization_catalog_syncs WHERE source='legacy-permissions'").WillReturnRows(sqlmock.NewRows([]string{"source"}))
	mock.ExpectQuery("SELECT p.id AS permission_id,a.id AS action_id").WillReturnRows(sqlmock.NewRows([]string{"permission_id", "action_id"}).AddRow("08896c3a-4c82-473e-90c8-b59f60446139", "18f4762f-f4f9-47d5-bd7a-6f07af436f58"))
	mock.ExpectQuery("SELECT id,deleted_at FROM permission_resource_actions").WithArgs("08896c3a-4c82-473e-90c8-b59f60446139", "18f4762f-f4f9-47d5-bd7a-6f07af436f58").WillReturnRows(sqlmock.NewRows([]string{"id", "deleted_at"}))
	mock.ExpectExec("INSERT INTO permission_resource_actions").WithArgs(sqlmock.AnyArg(), "08896c3a-4c82-473e-90c8-b59f60446139", "18f4762f-f4f9-47d5-bd7a-6f07af436f58", sqlmock.AnyArg(), codeActor, sqlmock.AnyArg(), codeActor).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO authorization_catalog_syncs").WithArgs(sqlmock.AnyArg(), codeActor, sqlmock.AnyArg(), codeActor).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	tx, err := sqlxDB.Beginx()
	require.NoError(t, err)
	require.NoError(t, backfillLegacyPermissionLinks(t.Context(), tx, generator))
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func expectStart(mock sqlmock.Sqlmock, buildTime, commit string) {
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.actor_id', $1, true)")).WithArgs(codeActor).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT build_time,commit_id FROM authorization_catalog_syncs").WillReturnRows(sqlmock.NewRows([]string{"build_time", "commit_id"}).AddRow(buildTime, commit))
}

func TestSyncInsertsCodeDefinitionsAndIsIdempotent(t *testing.T) {
	t.Parallel()
	resourceID, actionID := expectedIDs(t)
	syncer, mock := testSyncer(t, testRegistry(t))
	expectStart(mock, "", "")
	mock.ExpectQuery("SELECT id,name,description,scope,source,status,deleted_at FROM authorization_resources").WithArgs("tenant.member").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "scope", "source", "status", "deleted_at"}))
	mock.ExpectExec("INSERT INTO authorization_resources").WithArgs(resourceID, "tenant.member", "成员", "租户成员", resourceaction.ResourceScopeTenant, sqlmock.AnyArg(), codeActor, sqlmock.AnyArg(), codeActor).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id,name,description,source,status,deleted_at FROM authorization_resource_actions").WithArgs(resourceID, "read").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "source", "status", "deleted_at"}))
	mock.ExpectExec("INSERT INTO authorization_resource_actions").WithArgs(actionID, resourceID, "read", "查看", "查看成员", sqlmock.AnyArg(), codeActor, sqlmock.AnyArg(), codeActor).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE authorization_resource_actions SET status='disabled' WHERE source='code'").WithArgs(actionID).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE authorization_resources SET status='disabled' WHERE source='code'").WithArgs(resourceID).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT source FROM authorization_catalog_syncs WHERE source='legacy-permissions'").WillReturnRows(sqlmock.NewRows([]string{"source"}).AddRow("legacy-permissions"))
	mock.ExpectExec("UPDATE authorization_catalog_syncs SET build_time=").WithArgs(testBuildTime, "commit-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, syncer.Sync(t.Context(), testBuildTime, "commit-1"))

	expectStart(mock, testBuildTime, "commit-1")
	mock.ExpectQuery("SELECT id,name,description,scope,source,status,deleted_at FROM authorization_resources").WithArgs("tenant.member").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "scope", "source", "status", "deleted_at"}).AddRow(resourceID, "成员", "租户成员", "tenant", "code", "active", nil))
	mock.ExpectQuery("SELECT id,name,description,source,status,deleted_at FROM authorization_resource_actions").WithArgs(resourceID, "read").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "source", "status", "deleted_at"}).AddRow(actionID, "查看", "查看成员", "code", "active", nil))
	mock.ExpectExec("UPDATE authorization_resource_actions SET status='disabled' WHERE source='code'").WithArgs(actionID).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE authorization_resources SET status='disabled' WHERE source='code'").WithArgs(resourceID).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT source FROM authorization_catalog_syncs WHERE source='legacy-permissions'").WillReturnRows(sqlmock.NewRows([]string{"source"}).AddRow("legacy-permissions"))
	mock.ExpectCommit()
	require.NoError(t, syncer.Sync(t.Context(), testBuildTime, "commit-1"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSyncReplacesCodeDefinitionsAndPreservesManualRows(t *testing.T) {
	t.Parallel()
	resourceID, actionID := expectedIDs(t)
	syncer, mock := testSyncer(t, testRegistry(t))
	expectStart(mock, "", "")
	mock.ExpectQuery("SELECT id,name,description,scope,source,status,deleted_at FROM authorization_resources").WithArgs("tenant.member").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "scope", "source", "status", "deleted_at"}).AddRow(resourceID, "旧名称", "旧描述", "platform", "code", "disabled", nil))
	mock.ExpectExec("UPDATE authorization_resources SET name=").WithArgs("成员", "租户成员", resourceaction.ResourceScopeTenant, resourceID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id,name,description,source,status,deleted_at FROM authorization_resource_actions").WithArgs(resourceID, "read").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "source", "status", "deleted_at"}).AddRow(actionID, "旧名称", "旧描述", "code", "disabled", nil))
	mock.ExpectExec("UPDATE authorization_resource_actions SET name=").WithArgs("查看", "查看成员", actionID).WillReturnResult(sqlmock.NewResult(0, 1))
	// Both retirement queries are explicitly source='code'. A manual row is
	// never targeted even if its ID is absent from the code registry.
	mock.ExpectExec("UPDATE authorization_resource_actions SET status='disabled' WHERE source='code'").WithArgs(actionID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE authorization_resources SET status='disabled' WHERE source='code'").WithArgs(resourceID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT source FROM authorization_catalog_syncs WHERE source='legacy-permissions'").WillReturnRows(sqlmock.NewRows([]string{"source"}).AddRow("legacy-permissions"))
	mock.ExpectExec("UPDATE authorization_catalog_syncs SET build_time=").WithArgs(testBuildTime, "commit-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, syncer.Sync(t.Context(), testBuildTime, "commit-1"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSyncRemovedCodeDefinitions(t *testing.T) {
	t.Parallel()
	registry, err := resourceaction.NewRegistry(nil)
	require.NoError(t, err)
	syncer, mock := testSyncer(t, registry)
	expectStart(mock, "", "")
	mock.ExpectExec("UPDATE authorization_resource_actions SET status='disabled' WHERE source='code'").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("UPDATE authorization_resources SET status='disabled' WHERE source='code'").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT source FROM authorization_catalog_syncs WHERE source='legacy-permissions'").WillReturnRows(sqlmock.NewRows([]string{"source"}).AddRow("legacy-permissions"))
	mock.ExpectExec("UPDATE authorization_catalog_syncs SET build_time=").WithArgs(testBuildTime, "commit-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, syncer.Sync(t.Context(), testBuildTime, "commit-1"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSyncRejectsManualKeyConflict(t *testing.T) {
	t.Parallel()
	syncer, mock := testSyncer(t, testRegistry(t))
	expectStart(mock, "", "")
	mock.ExpectQuery("SELECT id,name,description,scope,source,status,deleted_at FROM authorization_resources").WithArgs("tenant.member").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "scope", "source", "status", "deleted_at"}).AddRow("manual-id", "手工资源", "", "tenant", "manual", "active", nil))
	mock.ExpectRollback()
	require.ErrorIs(t, syncer.Sync(t.Context(), testBuildTime, "commit-1"), ErrSourceConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSyncRejectsStaleBuild(t *testing.T) {
	t.Parallel()
	syncer, mock := testSyncer(t, testRegistry(t))
	expectStart(mock, "2026-10-14T08:00:00Z", "newer-commit")
	mock.ExpectRollback()
	require.ErrorIs(t, syncer.Sync(t.Context(), testBuildTime, "older-commit"), ErrStaleBuild)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCheckBuild(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		stored   revisionRow
		incoming revisionRow
		wantErr  error
	}{
		{name: "first deployment", incoming: revisionRow{BuildTime: testBuildTime, CommitID: "a"}},
		{name: "newer build", stored: revisionRow{BuildTime: testBuildTime, CommitID: "a"}, incoming: revisionRow{BuildTime: "2026-10-14T08:00:00Z", CommitID: "b"}},
		{name: "same build", stored: revisionRow{BuildTime: testBuildTime, CommitID: "a"}, incoming: revisionRow{BuildTime: testBuildTime, CommitID: "a"}},
		{name: "older build", stored: revisionRow{BuildTime: testBuildTime, CommitID: "a"}, incoming: revisionRow{BuildTime: "2026-10-12T08:00:00Z", CommitID: "b"}, wantErr: ErrStaleBuild},
		{name: "same timestamp different commit", stored: revisionRow{BuildTime: testBuildTime, CommitID: "a"}, incoming: revisionRow{BuildTime: testBuildTime, CommitID: "b"}, wantErr: ErrStaleBuild},
		{name: "unversioned build cannot replace release", stored: revisionRow{BuildTime: testBuildTime, CommitID: "a"}, incoming: revisionRow{BuildTime: "unknown", CommitID: "unknown"}, wantErr: ErrStaleBuild},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkBuild(tc.stored, tc.incoming)
			if tc.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.True(t, errors.Is(err, tc.wantErr))
			}
		})
	}
}
