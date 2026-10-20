package httptransport

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/tenant"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestPlatformTenantUpdateTargetReturnsOnlyEditableFields(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	mock.ExpectQuery(`SELECT id, code, name, description, status, version FROM tenants WHERE id = \? AND deleted_at IS NULL`).WithArgs("tenant-a").WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "description", "status", "version"}).AddRow("tenant-a", "demo", "Demo", "Description", "active", 3))
	handler := NewTenantHandler(tenant.New(tenant.NewRepository(sqlx.NewDb(raw, "sqlmock")), nil, nil, nil, nil, nil, nil, config.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/platform/tenants/update-target", strings.NewReader(`{"id":"tenant-a"}`)).WithContext(platformprincipal.SystemContext(t.Context(), "platform-admin"))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.AdminUpdateTarget(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, map[string]any{"id": "tenant-a", "code": "demo", "name": "Demo", "description": "Description", "status": "active", "version": float64(3)}, response.Body)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPlatformTenantUpdateTargetRejectsMissingID(t *testing.T) {
	t.Parallel()
	handler := NewTenantHandler(tenant.New(nil, nil, nil, nil, nil, nil, nil, config.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/platform/tenants/update-target", strings.NewReader(`{}`)).WithContext(platformprincipal.SystemContext(t.Context(), "platform-admin"))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.AdminUpdateTarget(ctx)
	require.NotEqual(t, http.StatusOK, recorder.Code)
}
