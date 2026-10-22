package httptransport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

type routeAuthorizerFunc func(context.Context, platformprincipal.Principal, platformauthz.Requirement) error

func (f routeAuthorizerFunc) Authorize(ctx context.Context, principal platformprincipal.Principal, requirement platformauthz.Requirement) error {
	return f(ctx, principal, requirement)
}

func TestIdempotencyReplayRequiresCurrentOperationAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resources, err := resourceaction.NewRegistryFromDefinitions(resourceaction.PlatformResourceDefinitions())
	require.NoError(t, err)
	schemas, err := datapermission.NewSchemaRegistry()
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := &fakeIdempotencyManager{decision: idempotency.Decision{
		State:    idempotency.StateCompleted,
		Response: []byte(`{"code":0,"message":"success","body":{"secret":"saved"}}`),
	}}
	allowed := false
	authorizationCalls := 0
	authorizer := routeAuthorizerFunc(func(_ context.Context, _ platformprincipal.Principal, requirement platformauthz.Requirement) error {
		authorizationCalls++
		require.Equal(t, "identity.session", requirement.Resource)
		if !allowed {
			return errors.New("grant revoked")
		}
		return nil
	})
	router := gin.New()
	router.Use(IdempotencyKey(logger), func(c *gin.Context) {
		ctx := platformprincipal.WithContext(c.Request.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
		c.Request = c.Request.WithContext(accesscontrol.WithCredentialScheme(ctx, accesscontrol.CredentialSchemeBearer))
		c.Next()
	})
	endpoint := jwtEndpoint("/api/v1/auth/sessions/logout-all", "identity.session", "logout", accesscontrol.DataPermissionNone)
	handlerCalled := false
	err = registerPOST(router.Group("/api/v1"), resources, schemas, authorizer, logger,
		IdempotencyExecution(manager, []string{"/api/v1/auth/sessions/logout-all"}, logger), endpoint,
		func(c *gin.Context) { handlerCalled = true; OK(c, nil) })
	require.NoError(t, err)
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sessions/logout-all", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer verified")
		req.Header.Set("Idempotency-Key", "replay-key")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}

	denied := request()
	require.Equal(t, http.StatusForbidden, denied.Code)
	require.NotContains(t, denied.Body.String(), "saved")
	require.Empty(t, manager.beginKey, "denied requests must not read idempotency state")
	require.False(t, handlerCalled)

	allowed = true
	replayed := request()
	require.Equal(t, http.StatusOK, replayed.Code)
	require.Contains(t, replayed.Body.String(), "saved")
	require.Equal(t, "replay-key", manager.beginKey)
	require.False(t, handlerCalled)
	require.Equal(t, 2, authorizationCalls)
}
