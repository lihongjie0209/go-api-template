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
	"github.com/lihongjie0209/go-api-template/internal/application"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestTenantApplicationRevokeTargetReturnsOnlyConfirmationFields(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, raw.Close())
	})
	now := time.Now()
	mock.ExpectQuery(`FROM tenant_application_grants g JOIN tenants t.*WHERE g.tenant_id=\? AND g.id=\? AND g.deleted_at IS NULL`).
		WithArgs("tenant-1", "grant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "tenant_name", "application_id", "application_code", "application_name",
			"application_icon", "application_home_path", "status", "starts_at", "expires_at",
			"created_at", "created_by", "updated_at", "updated_by", "version",
		}).AddRow("grant-1", "tenant-1", "测试租户", "app-1", "erp", "ERP", "icon", "/erp", "active", nil, nil, now, "admin", now, "admin", 3))
	access := application.NewTenantAccessService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil)
	handler := NewApplicationHandler(nil, access, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/platform/tenant-applications/revoke-target", strings.NewReader(`{"tenant_id":"tenant-1","id":"grant-1"}`)).WithContext(platformprincipal.SystemContext(t.Context(), "admin"))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.RevokeTenantApplicationTarget(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, map[string]any{
		"id": "grant-1", "tenant_id": "tenant-1", "tenant_name": "测试租户",
		"application_id": "app-1", "application_code": "erp", "application_name": "ERP",
		"status": "active", "version": float64(3),
	}, response.Body)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantApplicationRevokeTargetRejectsMissingID(t *testing.T) {
	t.Parallel()
	handler := NewApplicationHandler(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/platform/tenant-applications/revoke-target", strings.NewReader(`{"tenant_id":"tenant-1"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.RevokeTenantApplicationTarget(ctx)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
