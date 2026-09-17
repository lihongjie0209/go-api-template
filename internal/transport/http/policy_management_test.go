package httptransport

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestPBACManagementRouteRejectsDocumentScopeMismatchBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &PBACHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	router := gin.New()
	router.POST("/api/v1/pbac/tenant-policies/create", handler.Create)
	body := `{
		"policy": {
			"api_version": "authorization.platform/v1",
			"kind": "ActionPolicy",
			"metadata": {"code": "global-policy", "name": "Global policy"},
			"scope": {"type": "global"},
			"spec": {"subject": {"authenticated": true}, "resource": {"type": "identity.user"}, "actions": ["read"], "effect": "allow"}
		}
	}`

	response := performPolicyRequest(t, router, "/api/v1/pbac/tenant-policies/create", body)
	require.Equal(t, http.StatusForbidden, response.Code)
	require.Contains(t, response.Body.String(), "tenant policy access denied")
}

func TestDataPermissionManagementRouteRejectsDocumentScopeMismatchBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &DataPermissionHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	router := gin.New()
	router.POST("/api/v1/data-permissions/tenant-policies/create", handler.Create)
	body := `{
		"policy": {
			"api_version": "data-permission.platform/v1",
			"kind": "DataPermissionPolicy",
			"metadata": {"code": "global-data-policy", "name": "Global data policy"},
			"scope": {"type": "global"},
			"spec": {"subject": {"authenticated": true}, "resource": "tenant.member", "actions": ["read"], "condition": "resource.owner_id == subject.id", "effect": "allow"}
		}
	}`

	response := performPolicyRequest(t, router, "/api/v1/data-permissions/tenant-policies/create", body)
	require.Equal(t, http.StatusForbidden, response.Code)
	require.Contains(t, response.Body.String(), "data-permission policy scope denied")
}

func TestDataPermissionSimulationRejectsResourceFromAnotherTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &DataPermissionHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		ctx := platformprincipal.WithContext(c.Request.Context(), platformprincipal.Principal{
			ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1",
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.POST("/api/v1/data-permissions/tenant-policies/simulate", handler.Simulate)
	body := `{
		"policy": {
			"api_version": "data-permission.platform/v1",
			"kind": "DataPermissionPolicy",
			"metadata": {"code": "tenant-member-owner", "name": "Tenant member owner"},
			"scope": {"type": "tenant", "tenant_id": "tenant-1"},
			"spec": {"subject": {"authenticated": true}, "resource": "tenant.member", "actions": ["update"], "condition": "resource.owner_id == subject.id", "effect": "allow"}
		},
		"action": "update",
		"subject": {"id": "user-1", "authenticated": true, "tenant_id": "tenant-1"},
		"resource": {"type": "tenant.member", "tenant_id": "tenant-2"},
		"subject_attributes": {"id": "user-1"},
		"resource_attributes": {"owner_id": "user-1"}
	}`

	response := performPolicyRequest(t, router, "/api/v1/data-permissions/tenant-policies/simulate", body)
	require.Equal(t, http.StatusForbidden, response.Code)
	require.Contains(t, response.Body.String(), "data-permission policy scope denied")
}

func performPolicyRequest(t *testing.T, handler http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
