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
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/config"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestTenantPermissionTargetHandlerReturnsVersionedTenant(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := authorization.NewTenantAuthorizationService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, config.Config{}, nil)
	handler := NewTenantAuthorizationHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mock.ExpectQuery(`SELECT id,name,version FROM tenants WHERE id=\? AND deleted_at IS NULL`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "version"}).AddRow("tenant-1", "Tenant One", 7))

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "platform-user", Type: platformprincipal.TypeUser})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/platform/tenant-authorization/permissions/target", strings.NewReader(`{"tenant_id":"tenant-1"}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.TenantPermissionTarget(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Code int                                  `json:"code"`
		Body authorization.TenantPermissionTarget `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Zero(t, response.Code)
	require.Equal(t, authorization.TenantPermissionTarget{ID: "tenant-1", Name: "Tenant One", Version: 7}, response.Body)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantPermissionTargetHandlerRejectsMissingTenantID(t *testing.T) {
	t.Parallel()
	handler := NewTenantAuthorizationHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/platform/tenant-authorization/permissions/target", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.TenantPermissionTarget(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	var response struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.NotZero(t, response.Code)
}

func TestPlatformAdministratorTargetReturnsTrustedTenantName(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := authorization.NewTenantAuthorizationService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, config.Config{}, nil)
	handler := NewTenantAuthorizationHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mock.ExpectQuery(`SELECT id,name,version FROM tenants WHERE id=\? AND deleted_at IS NULL`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "version"}).AddRow("tenant-1", "Tenant One", 7))

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "platform-user", Type: platformprincipal.TypeUser})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/platform/tenant-authorization/administrators/target", strings.NewReader(`{"tenant_id":"tenant-1"}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.PlatformAdministratorTarget(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Code int                                  `json:"code"`
		Body authorization.TenantPermissionTarget `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Zero(t, response.Code)
	require.Equal(t, authorization.TenantPermissionTarget{ID: "tenant-1", Name: "Tenant One", Version: 7}, response.Body)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPlatformAdministratorTargetRejectsMissingTenantID(t *testing.T) {
	t.Parallel()
	handler := NewTenantAuthorizationHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/platform/tenant-authorization/administrators/target", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.PlatformAdministratorTarget(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestTenantApplicationTargetAliasesReturnTrustedName(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		call func(*TenantAuthorizationHandler, *gin.Context)
	}{
		{"read", "/api/v1/platform/tenant-applications/target/read", (*TenantAuthorizationHandler).TenantApplicationTargetRead},
		{"list", "/api/v1/platform/tenant-applications/target/list", (*TenantAuthorizationHandler).TenantApplicationTargetList},
		{"grant", "/api/v1/platform/tenant-applications/target/grant", (*TenantAuthorizationHandler).TenantApplicationTargetGrant},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			raw, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			service := authorization.NewTenantAuthorizationService(sqlx.NewDb(raw, "sqlmock"), nil, nil, nil, nil, nil, config.Config{}, nil)
			handler := NewTenantAuthorizationHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
			mock.ExpectQuery(`SELECT id,name,version FROM tenants WHERE id=\? AND deleted_at IS NULL`).
				WithArgs("tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"id", "name", "version"}).AddRow("tenant-1", "Tenant One", 7))
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			principalCtx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "platform-user", Type: platformprincipal.TypeUser})
			ctx.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{"tenant_id":"tenant-1"}`)).WithContext(principalCtx)
			ctx.Request.Header.Set("Content-Type", "application/json")
			test.call(handler, ctx)
			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Code int                                  `json:"code"`
				Body authorization.TenantPermissionTarget `json:"body"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Zero(t, response.Code)
			require.Equal(t, authorization.TenantPermissionTarget{ID: "tenant-1", Name: "Tenant One", Version: 7}, response.Body)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
