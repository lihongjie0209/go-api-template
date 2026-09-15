package routepolicy

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/stretchr/testify/require"
)

func TestServiceGetReturnsDisplayablePermissionReferences(t *testing.T) {
	t.Parallel()
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	service := &Service{db: sqlx.NewDb(database, "sqlmock")}
	now := time.Date(2026, time.September, 16, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	mock.ExpectQuery("SELECT id,route_id,expression,description,priority,status,created_at").
		WithArgs("route-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "route_id", "expression", "description", "priority", "status", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow("policy-1", "route-1", `authenticated && permissions["platform.user.page"]`, "", 0, "active", now, "user-1", now, "user-1", 3))
	mock.ExpectQuery("SELECT r.id,r.permission_id,p.permission_key,p.name AS permission_name,r.scope").
		WithArgs("policy-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "permission_id", "permission_key", "permission_name", "scope"}).
			AddRow("ref-1", "permission-1", "platform.user.page", "用户列表", "platform"))

	view, err := service.Get(t.Context(), "route-1")
	require.NoError(t, err)
	require.Equal(t, int64(3), view.Version)
	require.Equal(t, "用户列表", view.References[0].PermissionName)
	mock.ExpectClose()
	require.NoError(t, database.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestServicePageSupportsKeywordAndInFilters(t *testing.T) {
	t.Parallel()
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	service := &Service{db: sqlx.NewDb(database, "sqlmock")}
	mock.ExpectQuery(`SELECT count\(\*\).*LOWER\(r.path\) LIKE.*r.protocol IN \(\?, \?\).*r.status IN \(\?\)`).
		WithArgs("%user%", "%user%", "%user%", "http", "grpc", "active").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT r.id,r.protocol,r.method,r.path.*ORDER BY r.last_discovered_at DESC,r.id LIMIT \? OFFSET \?`).
		WithArgs("%user%", "%user%", "%user%", "http", "grpc", "active", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "protocol", "method", "path", "operation", "status", "source_version", "last_discovered_at", "policy_expression", "policy_status", "policy_version"}).
			AddRow("route-1", "http", "post", "/api/v1/users/page", "users.page", "active", "v1", time.Now(), nil, nil, nil))

	page, err := service.Page(t.Context(), PageInput{
		Request: pagination.Request{Keyword: "User"}, Protocols: []string{"http", "grpc"}, Statuses: []string{"active"},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Len(t, page.Items, 1)
	mock.ExpectClose()
	require.NoError(t, database.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestServicePageRejectsUnboundedFilters(t *testing.T) {
	t.Parallel()
	service := &Service{}
	protocols := make([]string, 21)
	_, err := service.Page(t.Context(), PageInput{Protocols: protocols})
	require.ErrorIs(t, err, ErrInvalid)
}
