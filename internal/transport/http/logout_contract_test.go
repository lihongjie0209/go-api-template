package httptransport

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
)

func TestLogoutRouteAcceptsRefreshCredentialWithoutAccessJWT(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := datapermission.NewSchemaRegistry()
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := gin.New()
	called := false
	err = registerPOST(router.Group("/api/v1"), resources, schemas, nil, logger, nil,
		publicEndpoint("/api/v1/auth/logout"), func(c *gin.Context) {
			called = true
			c.Status(http.StatusNoContent)
		})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader(`{"refresh_token":"credential"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || !called {
		t.Fatalf("public logout: status=%d handler_called=%v", response.Code, called)
	}

	protected := jwtEndpoint("/api/v1/auth/sessions/logout-all", "identity.session", "logout", accesscontrol.DataPermissionNone)
	if err := registerPOST(router.Group("/api/v1"), resources, schemas, nil, logger, nil, protected,
		func(c *gin.Context) { c.Status(http.StatusNoContent) }); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/sessions/logout-all", strings.NewReader(`{}`))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("protected logout-all: status=%d, want %d", response.Code, http.StatusUnauthorized)
	}
}
