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
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type fakeIdempotencyManager struct {
	decision    idempotency.Decision
	beginKey    string
	fingerprint string
	completed   *Response
	failed      *idempotency.Failure
}

func (*fakeIdempotencyManager) Enabled() bool { return true }
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

func idempotencyTestRouter(t *testing.T, manager idempotencyManager, calls *int) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router.Use(RequestID(), func(c *gin.Context) {
		c.Set("subject", "user-1")
		c.Request = c.Request.WithContext(idempotency.WithContext(c.Request.Context(), "operation-1"))
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

func TestIdempotencyExecutionCompletesUnifiedResponse(t *testing.T) {
	t.Parallel()
	manager := &fakeIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"}}
	calls := 0
	router := idempotencyTestRouter(t, manager, &calls)
	request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"name":"demo"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if calls != 1 || manager.beginKey != "operation-1" || manager.fingerprint == "" {
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
}

func TestExampleHTTPRequirementProtectsBusinessRoute(t *testing.T) {
	t.Parallel()
	requirement, ok := exampleHTTPRequirement("/api/v1/example/ping")
	if !ok || requirement.Resource != "example.hello" || requirement.Action != "ping" || requirement.Scope != platformauthz.ScopePrincipal {
		t.Fatalf("requirement = %+v, %v", requirement, ok)
	}
}

func TestAuthentication_PSKPrecedesSkipAndJWT(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	const key = "01234567890123456789012345678901"
	service := auth.New(config.Config{JWT: config.JWT{Issuer: "test", Secret: key, TTL: time.Hour}})
	for _, test := range []struct {
		name   string
		header string
		status int
	}{
		{name: "valid PSK", header: "PSK " + key, status: http.StatusOK},
		{name: "PSK route does not become public", status: http.StatusUnauthorized},
		{name: "bearer cannot access PSK route", header: "Bearer invalid", status: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			router := gin.New()
			router.Use(RequestID(), Authentication(service, slog.New(slog.NewTextHandler(io.Discard, nil)), config.Auth{
				SkipHTTPPaths: []string{"/api/v1/external/*"},
				PSK:           config.PSK{Enabled: true, Key: key, HTTPPaths: []string{"/api/v1/external/*"}},
			}))
			router.POST("/api/v1/external/callback", func(c *gin.Context) {
				value, ok := platformprincipal.FromContext(c.Request.Context())
				if test.status == http.StatusOK && (!ok || value.ID != "go-api-template:psk" || value.Type != platformprincipal.TypeServiceAccount) {
					c.AbortWithStatus(http.StatusInternalServerError)
					return
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

type oneByteReader struct{}

func (*oneByteReader) Read(buffer []byte) (int, error) { buffer[0] = 'x'; return 1, io.EOF }
