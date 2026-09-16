package httptransport

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/environment"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	appLimit "github.com/lihongjie0209/go-api-template/internal/ratelimit"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type fakeIdempotencyManager struct {
	decision     idempotency.Decision
	beginKey     string
	fingerprint  string
	completed    *Response
	failed       *idempotency.Failure
	leaseStarted bool
	aborted      bool
	maxBytes     int
}

func TestEnvironmentInjectsActiveProfile(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(Environment("test"))
	router.POST("/test", func(c *gin.Context) {
		profile, ok := environment.FromContext(c.Request.Context())
		if !ok || profile != "test" {
			t.Fatalf("environment = %q, %v", profile, ok)
		}
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
}

func (*fakeIdempotencyManager) Enabled() bool { return true }
func (m *fakeIdempotencyManager) MaxResponseBytes() int {
	if m.maxBytes > 0 {
		return m.maxBytes
	}
	return 1 << 20
}
func (m *fakeIdempotencyManager) Begin(_ context.Context, key, fingerprint string) (idempotency.Decision, error) {
	m.beginKey = key
	m.fingerprint = fingerprint
	return m.decision, nil
}
func (m *fakeIdempotencyManager) Complete(_ context.Context, _, _ string, response any) error {
	value, ok := response.(Response)
	if ok {
		m.completed = &value
	}
	return nil
}
func (m *fakeIdempotencyManager) Fail(_ context.Context, _, _ string, failure idempotency.Failure) error {
	m.failed = &failure
	return nil
}
func (m *fakeIdempotencyManager) Abort(context.Context, string, string) error {
	m.aborted = true
	return nil
}
func (m *fakeIdempotencyManager) StartLease(ctx context.Context, _, _ string) (context.Context, func() error, error) {
	m.leaseStarted = true
	return ctx, func() error { return nil }, nil
}

func idempotencyTestRouter(t *testing.T, manager idempotencyManager, calls *int) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router.Use(RequestID(), func(c *gin.Context) {
		c.Set("subject", "user-1")
		ctx := platformprincipal.WithContext(c.Request.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"})
		c.Request = c.Request.WithContext(idempotency.WithContext(ctx, "operation-1"))
		c.Next()
	}, IdempotencyExecution(manager, []string{"/test"}, logger))
	router.POST("/test", func(c *gin.Context) {
		*calls++
		var body map[string]any
		if err := c.ShouldBindJSON(&body); err != nil {
			Fail(c, logger, err)
			return
		}
		OK(c, body)
	})
	return router
}

func TestIdempotencyFingerprintIncludesTenantContext(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	fingerprint := func(tenantID string) string {
		request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"name":"demo"}`))
		ctx := platformprincipal.WithContext(request.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: tenantID, MembershipID: "member-1"})
		request = request.WithContext(ctx)
		ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginContext.Request = request
		ginContext.Params = gin.Params{}
		value, err := idempotencyFingerprint(ginContext)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	if fingerprint("tenant-1") == fingerprint("tenant-2") {
		t.Fatal("fingerprints for different tenant contexts must differ")
	}
}

func TestIdempotencyFingerprintCanonicalizesJSON(t *testing.T) {
	t.Parallel()
	fingerprint := func(body string) string {
		request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
		ctx := platformprincipal.WithContext(request.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
		request = request.WithContext(ctx)
		ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginContext.Request = request
		value, err := idempotencyFingerprint(ginContext)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	if fingerprint(`{"name":"demo","count":1}`) != fingerprint("{\n  \"count\": 1, \"name\": \"demo\"\n}") {
		t.Fatal("semantically equivalent JSON requests must have the same fingerprint")
	}
}

func TestIdempotencyExecutionCompletesUnifiedResponse(t *testing.T) {
	t.Parallel()
	manager := &fakeIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"}}
	calls := 0
	router := idempotencyTestRouter(t, manager, &calls)
	request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"name":"demo"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if calls != 1 || manager.beginKey != "operation-1" || manager.fingerprint == "" || !manager.leaseStarted {
		t.Fatalf("calls=%d key=%q fingerprint=%q", calls, manager.beginKey, manager.fingerprint)
	}
	if manager.completed == nil || manager.completed.RequestID != "" || manager.completed.Code != 0 {
		t.Fatalf("completed = %+v", manager.completed)
	}
}

func TestIdempotencyExecutionReplaysWithCurrentRequestID(t *testing.T) {
	t.Parallel()
	stored, err := json.Marshal(Response{Code: 0, Message: "success", Body: map[string]any{"id": "result-1"}, RequestID: "old-request"})
	if err != nil {
		t.Fatal(err)
	}
	manager := &fakeIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateCompleted, Response: stored}}
	calls := 0
	router := idempotencyTestRouter(t, manager, &calls)
	request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"name":"demo"}`))
	request.Header.Set("X-Request-ID", "new-request")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	var response Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || response.RequestID != "new-request" || recorder.Code != http.StatusOK {
		t.Fatalf("calls=%d status=%d response=%+v", calls, recorder.Code, response)
	}
}

func TestIdempotencyExecutionRejectsUnavailableDecisions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		decision idempotency.Decision
		code     int
	}{
		{name: "processing", decision: idempotency.Decision{State: idempotency.StateProcessing}, code: 30010},
		{name: "conflict", decision: idempotency.Decision{State: idempotency.StateConflict}, code: 30009},
		{name: "failed", decision: idempotency.Decision{State: idempotency.StateFailed, Failure: idempotency.Failure{Code: 10001, Message: "invalid", HTTPStatus: http.StatusBadRequest}}, code: 10001},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manager := &fakeIdempotencyManager{decision: test.decision}
			calls := 0
			router := idempotencyTestRouter(t, manager, &calls)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`)))
			var response Response
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if calls != 0 || response.Code != test.code {
				t.Fatalf("calls=%d response=%+v", calls, response)
			}
		})
	}
}

func TestIdempotencyExecutionBypassesUnconfiguredRoute(t *testing.T) {
	t.Parallel()
	manager := &fakeIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateConflict}}
	calls := 0
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(idempotency.WithContext(c.Request.Context(), "operation-1"))
		c.Next()
	}, IdempotencyExecution(manager, []string{"/create"}, slog.New(slog.NewTextHandler(io.Discard, nil))))
	router.POST("/list", func(c *gin.Context) { calls++; OK(c, nil) })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/list", nil))
	if calls != 1 || manager.beginKey != "" || recorder.Code != http.StatusOK {
		t.Fatalf("calls=%d begin_key=%q status=%d", calls, manager.beginKey, recorder.Code)
	}
}

func TestIdempotencyExecutionAbortsRetryableFailure(t *testing.T) {
	t.Parallel()
	manager := &fakeIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"}}
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		ctx := idempotency.WithContext(c.Request.Context(), "operation-1")
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}, IdempotencyExecution(manager, []string{"/test"}, logger))
	router.POST("/test", func(c *gin.Context) {
		Fail(c, logger, apperror.Unavailable("temporary failure", nil))
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`))
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !manager.aborted || manager.failed != nil {
		t.Fatalf("status=%d aborted=%v failed=%+v", recorder.Code, manager.aborted, manager.failed)
	}
}

func TestIdempotencyExecutionBoundsCapturedResponse(t *testing.T) {
	t.Parallel()
	manager := &fakeIdempotencyManager{
		decision: idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"},
		maxBytes: 32,
	}
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(idempotency.WithContext(c.Request.Context(), "operation-1"))
		c.Next()
	}, IdempotencyExecution(manager, []string{"/test"}, logger))
	router.POST("/test", func(c *gin.Context) { OK(c, strings.Repeat("x", 128)) })

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusOK || !manager.aborted || manager.completed != nil || manager.failed != nil {
		t.Fatalf("status=%d aborted=%v completed=%+v failed=%+v", recorder.Code, manager.aborted, manager.completed, manager.failed)
	}
}

func TestRequestID(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())
	router.POST("/test", func(c *gin.Context) { OK(c, nil) })
	request := httptest.NewRequest(http.MethodPost, "/test", nil)
	request.Header.Set("X-Request-ID", "client-request-1")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if got := recorder.Header().Get("X-Request-ID"); got != "client-request-1" {
		t.Fatalf("X-Request-ID = %q", got)
	}
	var response Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.RequestID != "client-request-1" {
		t.Fatalf("request_id = %q", response.RequestID)
	}
	invalid := httptest.NewRequest(http.MethodPost, "/test", nil)
	invalid.Header.Set("X-Request-ID", "contains spaces")
	invalidRecorder := httptest.NewRecorder()
	router.ServeHTTP(invalidRecorder, invalid)
	generated := invalidRecorder.Header().Get("X-Request-ID")
	if generated == "contains spaces" || !requestid.Valid(generated) {
		t.Fatalf("generated X-Request-ID = %q", generated)
	}
}

func TestDatabaseAuthenticationVerifiesSuppliedCredentials(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	const key = "01234567890123456789012345678901"
	service := auth.New(config.Config{})
	for _, test := range []struct {
		name   string
		header string
		status int
	}{
		{name: "valid PSK", header: "PSK " + key, status: http.StatusOK},
		{name: "missing credential remains anonymous", status: http.StatusOK},
		{name: "bearer cannot access PSK route", header: "Bearer invalid", status: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			router := gin.New()
			router.Use(RequestID(), DatabaseAuthentication(service, slog.New(slog.NewTextHandler(io.Discard, nil)), config.Config{App: config.App{Name: "orders-service"}, Auth: config.Auth{PSK: config.PSK{Enabled: true, Key: key}}}))
			router.POST("/api/v1/external/callback", func(c *gin.Context) {
				value, ok := platformprincipal.FromContext(c.Request.Context())
				if strings.HasPrefix(test.header, "PSK ") && (!ok || value.ID != "orders-service:psk" || value.Type != platformprincipal.TypeServiceAccount) {
					c.AbortWithStatus(http.StatusInternalServerError)
					return
				}
				if strings.HasPrefix(test.header, "PSK ") {
					scheme, schemeOK := accesscontrol.CredentialSchemeFromContext(c.Request.Context())
					if !schemeOK || scheme != accesscontrol.CredentialSchemePSK {
						c.AbortWithStatus(http.StatusInternalServerError)
						return
					}
				}
				OK(c, nil)
			})
			request := httptest.NewRequest(http.MethodPost, "/api/v1/external/callback", nil)
			request.Header.Set("Authorization", test.header)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
		})
	}
}

func TestRequireJSON(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID(), RequireJSON())
	router.POST("/test", func(c *gin.Context) { OK(c, nil) })
	request := httptest.NewRequest(http.MethodPost, "/test", io.NopCloser(&oneByteReader{}))
	request.ContentLength = 1
	request.Header.Set("Content-Type", "text/plain")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestTimeoutPropagatesCancellation(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := gin.New()
	router.Use(RequestID(), Timeout(time.Millisecond, logger))
	router.POST("/test", func(c *gin.Context) { <-c.Request.Context().Done() })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/test", nil))
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusGatewayTimeout)
	}
}

func TestLoginRateLimitNeverFailsOpen(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	limiter := appLimit.New(nil, config.Config{RateLimit: config.RateLimit{Enabled: true, FailOpen: true}}, nil)
	rule := config.RateLimitRule{Rate: 1, Burst: 1, Period: time.Minute}
	for _, test := range []struct {
		name      string
		dimension string
		status    int
		called    bool
	}{
		{name: "login fails closed", dimension: "login", status: http.StatusServiceUnavailable},
		{name: "ordinary API follows fail open", dimension: "api", status: http.StatusOK, called: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			router := gin.New()
			router.Use(RequestID(), RateLimit(limiter, rule, test.dimension, func(*gin.Context) string { return "key" }, logger))
			router.POST("/test", func(c *gin.Context) { called = true; OK(c, nil) })
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/test", nil))
			if recorder.Code != test.status || called != test.called {
				t.Fatalf("status=%d called=%t", recorder.Code, called)
			}
		})
	}
}

func TestSecurityHeadersAndCORSWhitelist(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID(), SecurityHeaders(), CORS(config.CORS{Enabled: true, AllowedOrigins: []string{"https://admin.example.com"}, AllowedHeaders: []string{"Content-Type"}, ExposedHeaders: []string{"X-Request-ID"}, MaxAge: time.Hour}))
	router.POST("/test", func(c *gin.Context) { OK(c, nil) })

	allowed := httptest.NewRequest(http.MethodPost, "/test", nil)
	allowed.Header.Set("Origin", "https://admin.example.com")
	allowedRecorder := httptest.NewRecorder()
	router.ServeHTTP(allowedRecorder, allowed)
	if allowedRecorder.Code != http.StatusOK || allowedRecorder.Header().Get("Access-Control-Allow-Origin") != "https://admin.example.com" || allowedRecorder.Header().Get("X-Content-Type-Options") != "nosniff" || allowedRecorder.Header().Get("Permissions-Policy") == "" || allowedRecorder.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" || allowedRecorder.Header().Get("Strict-Transport-Security") == "" {
		t.Fatalf("allowed response status=%d headers=%v", allowedRecorder.Code, allowedRecorder.Header())
	}

	denied := httptest.NewRequest(http.MethodPost, "/test", nil)
	denied.Header.Set("Origin", "https://evil.example.com")
	deniedRecorder := httptest.NewRecorder()
	router.ServeHTTP(deniedRecorder, denied)
	if deniedRecorder.Code != http.StatusForbidden {
		t.Fatalf("denied status=%d", deniedRecorder.Code)
	}
}

func TestSwaggerSecurityHeadersOverrideStrictAPIContentPolicy(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(SecurityHeaders())
	group := router.Group("/swagger", SwaggerSecurityHeaders())
	group.GET("/index.html", func(c *gin.Context) { c.Status(http.StatusOK) })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil))
	policy := recorder.Header().Get("Content-Security-Policy")
	if recorder.Code != http.StatusOK || !strings.Contains(policy, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("status=%d policy=%q", recorder.Code, policy)
	}
}

func TestPprofBearerProtection(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	const token = "01234567890123456789012345678901"
	for _, test := range []struct {
		name   string
		header string
		status int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "wrong scheme", header: "PSK " + token, status: http.StatusUnauthorized},
		{name: "wrong token", header: "Bearer 01234567890123456789012345678902", status: http.StatusUnauthorized},
		{name: "valid", header: "Bearer " + token, status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			registerPprof(router.Group("/debug/pprof", pprofAuth(token)))
			request := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
			request.Header.Set("Authorization", test.header)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status=%d, want %d", recorder.Code, test.status)
			}
		})
	}
}

func TestUnknownRouteAndMethodUseUnifiedEnvelope(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := gin.New()
	router.Use(RequestID())
	configureRouterContract(router, logger)
	router.POST("/api/v1/known", func(c *gin.Context) { OK(c, nil) })

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantCode   int
	}{
		{name: "unknown route", method: http.MethodPost, path: "/api/v1/missing", wantStatus: http.StatusNotFound, wantCode: apperror.CodeNotFound},
		{name: "unknown method", method: http.MethodGet, path: "/api/v1/known", wantStatus: http.StatusMethodNotAllowed, wantCode: apperror.CodeInvalidArgument},
		{name: "trailing slash is not redirected", method: http.MethodPost, path: "/api/v1/known/", wantStatus: http.StatusNotFound, wantCode: apperror.CodeNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
			var response Response
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
			}
			if recorder.Code != test.wantStatus || response.Code != test.wantCode || response.RequestID == "" {
				t.Fatalf("status=%d response=%+v", recorder.Code, response)
			}
		})
	}
}

type oneByteReader struct{}

func (*oneByteReader) Read(buffer []byte) (int, error) { buffer[0] = 'x'; return 1, io.EOF }
