package routepolicy

import (
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	"github.com/stretchr/testify/require"
)

func TestRepositoryLoadJoinsPermissionReferences(t *testing.T) {
	t.Parallel()
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	repository := NewRepository(sqlx.NewDb(database, "sqlmock"))

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id,route_id,expression,version FROM route_policy_definitions WHERE status='active' AND deleted_at IS NULL ORDER BY route_id")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "route_id", "expression", "version"}).
			AddRow("policy-1", "route-1", `authenticated && permissions["platform.user.page"]`, 2))
	mock.ExpectQuery("SELECT r\\.policy_id,p\\.id,p\\.permission_key,p\\.resource,p\\.action,r\\.scope").
		WillReturnRows(sqlmock.NewRows([]string{"policy_id", "id", "permission_key", "resource", "action", "scope"}).
			AddRow("policy-1", "permission-1", "platform.user.page", "platform.user", "page", "platform"))

	definitions, err := repository.Load(t.Context())
	require.NoError(t, err)
	require.Len(t, definitions, 1)
	require.Equal(t, platformauthz.ScopePlatform, definitions[0].Permissions["platform.user.page"].Scope)
	mock.ExpectClose()
	require.NoError(t, database.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryRevisionIncludesDeletes(t *testing.T) {
	t.Parallel()
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	repository := NewRepository(sqlx.NewDb(database, "sqlmock"))
	expected := time.Date(2026, time.September, 15, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`(?s)SELECT max\(updated_at\) FROM \(.*SELECT updated_at FROM route_policy_definitions.*UNION ALL.*SELECT p.updated_at FROM permissions p.*route_policy_revisions`).
		WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(expected))

	revision, err := repository.Revision(t.Context())
	require.NoError(t, err)
	require.Equal(t, expected, revision)
	mock.ExpectClose()
	require.NoError(t, database.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}
