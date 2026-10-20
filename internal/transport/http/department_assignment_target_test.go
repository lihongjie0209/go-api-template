package httptransport

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/tenant"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestDepartmentAssignmentTargetReturnsNarrowVersionedResponse(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	now := time.Now()
	mock.ExpectQuery(`SELECT .* FROM tenant_departments td WHERE td.tenant_id=\? AND td.id=\? AND td.deleted_at IS NULL AND \(1 = 1\)`).
		WithArgs("tenant-a", "department-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}).
			AddRow("department-a", "tenant-a", nil, "engineering", "Engineering", 0, now, "creator", now, "updater", 7))
	handler := NewDepartmentHandler(tenant.NewDepartmentService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, config.Config{}, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "operator", Type: platformprincipal.TypeSystem, TenantID: "tenant-a", MembershipID: "membership-a"})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tenant-departments/members/target", strings.NewReader(`{"id":"department-a"}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.AssignmentTarget(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Code int                        `json:"code"`
		Body DepartmentAssignmentTarget `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Zero(t, response.Code)
	require.Equal(t, DepartmentAssignmentTarget{ID: "department-a", Name: "Engineering", Version: 7}, response.Body)
	require.NotContains(t, recorder.Body.String(), "tenant_id")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDepartmentAssignmentTargetRejectsMissingID(t *testing.T) {
	t.Parallel()
	handler := NewDepartmentHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tenant-departments/members/target", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.AssignmentTarget(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestDepartmentAssignmentTargetHidesOtherTenantRows(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT .* FROM tenant_departments td WHERE td.tenant_id=\? AND td.id=\? AND td.deleted_at IS NULL AND \(1 = 1\)`).
		WithArgs("tenant-a", "department-other-tenant").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "parent_id", "code", "name", "sort_order", "created_at", "created_by", "updated_at", "updated_by", "version"}))
	handler := NewDepartmentHandler(tenant.NewDepartmentService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, config.Config{}, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "operator", Type: platformprincipal.TypeSystem, TenantID: "tenant-a", MembershipID: "membership-a"})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tenant-departments/members/target", strings.NewReader(`{"id":"department-other-tenant"}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.AssignmentTarget(c)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "department-other-tenant")
	require.NoError(t, mock.ExpectationsWereMet())
}
