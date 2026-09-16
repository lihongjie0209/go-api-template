package routepolicy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
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

	view, err := service.Get(platformprincipal.SystemContext(t.Context(), "admin"), "route-1")
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

	page, err := service.Page(platformprincipal.SystemContext(t.Context(), "admin"), PageInput{
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
	_, err := service.Page(platformprincipal.SystemContext(t.Context(), "admin"), PageInput{Protocols: protocols})
	require.ErrorIs(t, err, ErrInvalid)
}

func TestBoundedRouteAuditNamePreservesUTF8(t *testing.T) {
	t.Parallel()
	value := "http POST /" + strings.Repeat("界", 300)
	got := boundedRouteAuditName(value)
	require.LessOrEqual(t, len(got), maxRouteAuditNameBytes)
	require.True(t, utf8.ValidString(got))
	require.Equal(t, value[:100], boundedRouteAuditName(value[:100]))
}

func TestSetRollsBackWhenTransactionalAuditFails(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	recorder := &failingOperationRecorder{err: errors.New("outbox unavailable")}
	compiler, err := NewCompiler()
	require.NoError(t, err)
	service := &Service{
		db: sqlxDB, transactor: database.NewTransactor(sqlxDB), compiler: compiler,
		operations: recorder, security: routeSecurityRecorder{},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT protocol,method,path FROM route_definitions`).WithArgs("route-1").WillReturnRows(sqlmock.NewRows([]string{"protocol", "method", "path"}).AddRow("http", "post", "/api/v1/users/page"))
	mock.ExpectExec(`INSERT INTO route_policy_definitions`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT id,permission_id,deleted_at FROM route_policy_permission_refs`).WithArgs("route-1").WillReturnRows(sqlmock.NewRows([]string{"id", "permission_id", "deleted_at"}))
	mock.ExpectRollback()

	_, err = service.Set(platformprincipal.SystemContext(t.Context(), "admin"), SetInput{RouteID: "route-1", Expression: "authenticated", Status: "active"})
	require.ErrorContains(t, err, "outbox unavailable")
	require.Equal(t, 1, recorder.transactionalCalls)
	require.Equal(t, 1, recorder.failureCalls)
	require.Equal(t, "http POST /api/v1/users/page", recorder.lastEntry.ResourceName)
	require.NoError(t, mock.ExpectationsWereMet())
}

type failingOperationRecorder struct {
	err                error
	transactionalCalls int
	failureCalls       int
	lastEntry          operationlog.Entry
}

func (*failingOperationRecorder) Enabled() bool { return true }
func (r *failingOperationRecorder) Record(_ context.Context, entry operationlog.Entry) error {
	if !entry.Succeeded {
		r.failureCalls++
	}
	return nil
}
func (r *failingOperationRecorder) RecordTx(_ context.Context, _ *sqlx.Tx, entry operationlog.Entry) error {
	r.transactionalCalls++
	r.lastEntry = entry
	return r.err
}

type routeSecurityRecorder struct{}

func (routeSecurityRecorder) Enabled() bool                                   { return true }
func (routeSecurityRecorder) FailClosed() bool                                { return true }
func (routeSecurityRecorder) Record(context.Context, securitylog.Entry) error { return nil }
func (routeSecurityRecorder) RecordTx(context.Context, *sqlx.Tx, securitylog.Entry) error {
	return nil
}
