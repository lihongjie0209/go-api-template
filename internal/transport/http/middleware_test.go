package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/environment"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	appLimit "github.com/lihongjie0209/go-api-template/internal/ratelimit"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	"github.com/lihongjie0209/go-api-template/internal/testutil"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/redis/go-redis/v9"
)

type fakeIdempotencyManager struct {
	decision                idempotency.Decision
	beginKey                string
	fingerprint             string
	completed               *Response
	completedStatus         int
	completeErr             error
	failureErr              error
	abortErr                error
	failed                  *idempotency.Failure
	leaseStarted            bool
	leaseStopped            bool
	cancelLeaseOnStop       bool
	leaseStopErr            error
	leaseStartErr           error
	cancelLeaseBeforeReturn bool
	aborted                 bool
	maxBytes                int
	onComplete              func()
	onBegin                 func(context.Context)
	onStartLease            func(context.Context)
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

func TestPasswordChangeGateFailsClosedOutsideCredentialRecovery(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name       string
		path       string
		required   bool
		wantStatus int
	}{
		{name: "ordinary session", path: "/api/v1/users/page", wantStatus: http.StatusNoContent},
		{name: "restricted ordinary API", path: "/api/v1/users/page", required: true, wantStatus: http.StatusForbidden},
		{name: "restricted password change", path: "/api/v1/auth/password/change", required: true, wantStatus: http.StatusNoContent},
		{name: "restricted refresh", path: "/api/v1/auth/refresh", required: true, wantStatus: http.StatusNoContent},
		{name: "restricted logout", path: "/api/v1/auth/logout", required: true, wantStatus: http.StatusNoContent},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(passwordChangeRequiredKey, test.required)
				c.Next()
			}, PasswordChangeGate(logger))
			router.POST(test.path, func(c *gin.Context) { c.Status(http.StatusNoContent) })
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{}`))
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
		})
	}
}

func (*fakeIdempotencyManager) Enabled() bool { return true }
func (m *fakeIdempotencyManager) MaxResponseBytes() int {
	if m.maxBytes > 0 {
		return m.maxBytes
	}
	return 1 << 20
}
func (m *fakeIdempotencyManager) Begin(ctx context.Context, key, fingerprint string) (idempotency.Decision, error) {
	m.beginKey = key
	m.fingerprint = fingerprint
	if m.onBegin != nil {
		m.onBegin(ctx)
	}
	return m.decision, nil
}

func TestIdempotencyExecutionDoesNotReplayAfterDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	saved, err := json.Marshal(idempotentHTTPResult{HTTPStatus: http.StatusOK, Response: Response{Code: apperror.CodeOK, Message: "success", Body: "stale"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		decision idempotency.Decision
		aborted  bool
	}{
		{name: "completed replay", decision: idempotency.Decision{State: idempotency.StateCompleted, Response: saved}},
		{name: "acquired execution", decision: idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"}, aborted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &fakeIdempotencyManager{decision: test.decision, onBegin: func(ctx context.Context) { <-ctx.Done() }}
			called := false
			router := gin.New()
			router.Use(RequestID(), Timeout(time.Millisecond, logger), func(c *gin.Context) {
				c.Request = c.Request.WithContext(idempotency.WithContext(c.Request.Context(), "operation-1"))
				c.Next()
			}, IdempotencyExecution(manager, []string{"/test"}, logger))
			router.POST("/test", func(c *gin.Context) { called = true; OK(c, "late") })
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`)))
			var body Response
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusGatewayTimeout || body.Code != apperror.CodeRequestTimeout || body.RequestID == "" || called || manager.aborted != test.aborted || strings.Contains(response.Body.String(), "stale") {
				t.Fatalf("late decision response=%d %s called=%t aborted=%t", response.Code, response.Body.String(), called, manager.aborted)
			}
		})
	}
}

func TestIdempotencyExecutionDoesNotFlushLateSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := &fakeIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"}}
	router := gin.New()
	router.Use(RequestID(), Timeout(time.Millisecond, logger), func(c *gin.Context) {
		c.Request = c.Request.WithContext(idempotency.WithContext(c.Request.Context(), "operation-1"))
		c.Next()
	}, IdempotencyExecution(manager, []string{"/test"}, logger))
	router.POST("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, Response{Code: apperror.CodeOK, Message: "success", Body: "late", RequestID: requestID(c)})
		<-c.Request.Context().Done()
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`)))
	var body Response
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusGatewayTimeout || body.Code != apperror.CodeRequestTimeout || body.RequestID == "" || strings.Contains(response.Body.String(), "late") || manager.completed == nil {
		t.Fatalf("late captured response=%d %s completed=%+v", response.Code, response.Body.String(), manager.completed)
	}
}
func (m *fakeIdempotencyManager) Complete(_ context.Context, _, _ string, response any) error {
	if m.onComplete != nil {
		m.onComplete()
	}
	value, ok := response.(idempotentHTTPResult)
	if ok {
		m.completed = &value.Response
		m.completedStatus = value.HTTPStatus
	}
	return m.completeErr
}

func TestIdempotencyExecutionDoesNotLogRedisErrorDetails(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	manager := &fakeIdempotencyManager{
		decision:    idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"},
		completeErr: errors.New("redis password=secret-value"),
	}
	router := gin.New()
	router.Use(RequestID(), func(c *gin.Context) {
		ctx := platformprincipal.WithContext(c.Request.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
		c.Request = c.Request.WithContext(idempotency.WithContext(ctx, "operation-1"))
		c.Next()
	}, IdempotencyExecution(manager, []string{"/test"}, logger))
	router.POST("/test", func(c *gin.Context) {
		c.Header("Set-Cookie", "session=should-not-leak")
		OK(c, gin.H{})
	})
	request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`))
	request.Header.Set("Idempotency-Key", "key-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || manager.completed == nil {
		t.Fatalf("status = %d, completed = %v", response.Code, manager.completed)
	}
	var body Response
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code == apperror.CodeOK || body.RequestID == "" {
		t.Fatalf("failed persistence leaked success response: %+v", body)
	}
	if response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("failed persistence leaked success cookie: %q", response.Header().Get("Set-Cookie"))
	}
	if strings.Contains(output.String(), "secret-value") || !strings.Contains(output.String(), "idempotency_state_failed") {
		t.Fatalf("idempotency failure log leaked Redis detail or omitted safe code: %s", output.String())
	}
}
func (m *fakeIdempotencyManager) Fail(_ context.Context, _, _ string, failure idempotency.Failure) error {
	m.failed = &failure
	return m.failureErr
}
func (m *fakeIdempotencyManager) Abort(context.Context, string, string) error {
	m.aborted = true
	return m.abortErr
}
func (m *fakeIdempotencyManager) StartLease(ctx context.Context, _, _ string) (context.Context, func() error, error) {
	m.leaseStarted = true
	if m.onStartLease != nil {
		m.onStartLease(ctx)
	}
	if m.leaseStartErr != nil {
		return nil, nil, m.leaseStartErr
	}
	if m.cancelLeaseBeforeReturn {
		leaseCtx, cancel := context.WithCancel(ctx)
		cancel()
		return leaseCtx, func() error { m.leaseStopped = true; return nil }, nil
	}
	if m.cancelLeaseOnStop {
		leaseCtx, cancel := context.WithCancel(ctx)
		return leaseCtx, func() error { m.leaseStopped = true; cancel(); return m.leaseStopErr }, nil
	}
	return ctx, func() error { m.leaseStopped = true; return m.leaseStopErr }, nil
}

func TestIdempotencyExecutionReleasesAcquisitionWhenLeaseCannotStart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, test := range []struct {
		name               string
		startErr           error
		cancelBeforeReturn bool
	}{
		{name: "lease start failed", startErr: errors.New("redis lease unavailable")},
		{name: "lease canceled before handler", cancelBeforeReturn: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &fakeIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"}, leaseStartErr: test.startErr, cancelLeaseBeforeReturn: test.cancelBeforeReturn}
			called := false
			router := gin.New()
			router.Use(RequestID(), func(c *gin.Context) {
				c.Request = c.Request.WithContext(idempotency.WithContext(c.Request.Context(), "operation-1"))
				c.Next()
			}, IdempotencyExecution(manager, []string{"/test"}, logger))
			router.POST("/test", func(c *gin.Context) { called = true; OK(c, "late") })
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`)))
			var body Response
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusServiceUnavailable || body.Code != apperror.CodeDependencyUnavailable || called || !manager.aborted || manager.leaseStopped != test.cancelBeforeReturn {
				t.Fatalf("failed lease start response=%d %s called=%t aborted=%t stopped=%t", response.Code, response.Body.String(), called, manager.aborted, manager.leaseStopped)
			}
		})
	}
}

func TestIdempotencyExecutionAbortsWhenDeadlineExpiresDuringLeaseStart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := &fakeIdempotencyManager{
		decision:     idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"},
		onStartLease: func(ctx context.Context) { <-ctx.Done() },
	}
	called := false
	router := gin.New()
	router.Use(RequestID(), Timeout(50*time.Millisecond, logger), func(c *gin.Context) {
		c.Request = c.Request.WithContext(idempotency.WithContext(c.Request.Context(), "operation-1"))
		c.Next()
	}, IdempotencyExecution(manager, []string{"/test"}, logger))
	router.POST("/test", func(c *gin.Context) { called = true; OK(c, "late") })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`)))
	var body Response
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusGatewayTimeout || body.Code != apperror.CodeRequestTimeout || called || !manager.leaseStarted || !manager.leaseStopped || !manager.aborted {
		t.Fatalf("lease startup response=%d %s called=%t started=%t stopped=%t aborted=%t", response.Code, response.Body.String(), called, manager.leaseStarted, manager.leaseStopped, manager.aborted)
	}
}

func TestIdempotencyExecutionUsesRequestContextAfterLeaseStop(t *testing.T) {
	for _, test := range []struct {
		name       string
		stopErr    error
		wantStatus int
	}{
		{name: "healthy lease stop", wantStatus: http.StatusOK},
		{name: "lease stop failure", stopErr: errors.New("lease lost"), wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &fakeIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"}, cancelLeaseOnStop: true, leaseStopErr: test.stopErr}
			calls := 0
			router := idempotencyTestRouter(t, manager, &calls)
			request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"name":"demo"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus || calls != 1 || !manager.leaseStopped || response.Body.Len() == 0 {
				t.Fatalf("lease stop response=%d body=%s calls=%d stopped=%t", response.Code, response.Body.String(), calls, manager.leaseStopped)
			}
		})
	}
}

func TestIdempotencyExecutionRealRedisLeaseStopStillReturnsSuccessAndReplay(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	manager := idempotency.New(client, config.Config{
		Runtime: config.Runtime{ActiveProfile: "test"},
		App:     config.App{Name: "orders-service"},
		Idempotency: config.Idempotency{
			Enabled: true, ProcessingTTL: time.Minute, ResultTTL: time.Hour,
			FailureTTL: time.Minute, MaxResponseBytes: 1024,
		},
	}, nil)
	calls := 0
	router := idempotencyTestRouter(t, manager, &calls)
	for _, requestID := range []string{"first-request", "retry-request"} {
		request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"name":"demo"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Request-ID", requestID)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		var body Response
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || body.Code != apperror.CodeOK || body.RequestID != requestID || calls != 1 {
			t.Fatalf("%s response=%d %s calls=%d", requestID, response.Code, response.Body.String(), calls)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	emptyCalls := 0
	emptyRouter := gin.New()
	emptyRouter.Use(RequestID(), func(c *gin.Context) {
		c.Request = c.Request.WithContext(idempotency.WithContext(c.Request.Context(), "logout-all-key"))
		c.Next()
	}, IdempotencyExecution(manager, []string{"/logout-all"}, logger))
	emptyRouter.POST("/logout-all", func(c *gin.Context) { emptyCalls++; OK(c, gin.H{}) })
	for _, requestID := range []string{"empty-first", "empty-retry"} {
		request := httptest.NewRequest(http.MethodPost, "/logout-all", nil)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Request-ID", requestID)
		response := httptest.NewRecorder()
		emptyRouter.ServeHTTP(response, request)
		var body Response
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || body.Code != apperror.CodeOK || body.RequestID != requestID || emptyCalls != 1 {
			t.Fatalf("bodyless %s response=%d %s calls=%d", requestID, response.Code, response.Body.String(), emptyCalls)
		}
	}
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
	if fingerprint("") != fingerprint(" \n\t ") {
		t.Fatal("empty and whitespace-only command bodies must share one fingerprint")
	}
	if fingerprint("") == fingerprint("null") || fingerprint("") == fingerprint("{}") {
		t.Fatal("empty command body must remain distinct from JSON values")
	}
}

func TestIdempotencyExecutionBoundsRequestBeforeRedisAndHandler(t *testing.T) {
	manager := &fakeIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"}}
	calls := 0
	router := idempotencyTestRouter(t, manager, &calls)
	request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(strings.Repeat("x", maxIdempotencyRequestBytes+1)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var body Response
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusBadRequest || body.Code != apperror.CodeInvalidArgument || body.RequestID == "" || calls != 0 || manager.beginKey != "" {
		t.Fatalf("oversized idempotent request=%d %s calls=%d begin=%q", response.Code, response.Body.String(), calls, manager.beginKey)
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
	manager.onComplete = func() {
		if recorder.Body.Len() != 0 {
			t.Error("success response was sent before idempotency result was persisted")
		}
	}
	router.ServeHTTP(recorder, request)
	if calls != 1 || manager.beginKey != "operation-1" || manager.fingerprint == "" || !manager.leaseStarted {
		t.Fatalf("calls=%d key=%q fingerprint=%q", calls, manager.beginKey, manager.fingerprint)
	}
	if manager.completed == nil || manager.completed.RequestID != "" || manager.completed.Code != 0 || manager.completedStatus != http.StatusOK {
		t.Fatalf("completed = %+v, status = %d", manager.completed, manager.completedStatus)
	}
}

func TestIdempotencyExecutionPreservesCreatedStatusOnReplay(t *testing.T) {
	t.Parallel()
	manager := &fakeIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := gin.New()
	router.Use(RequestID(), func(c *gin.Context) {
		c.Request = c.Request.WithContext(idempotency.WithContext(c.Request.Context(), "create-key"))
		c.Next()
	}, IdempotencyExecution(manager, []string{"/create"}, logger))
	called := 0
	router.POST("/create", func(c *gin.Context) {
		called++
		c.JSON(http.StatusCreated, Response{Code: 0, Message: "success", Body: gin.H{"id": "created-1"}, RequestID: requestID(c)})
	})
	request := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/create", strings.NewReader(`{}`))
		req.Header.Set("X-Request-ID", id)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	first := request("first-request")
	if first.Code != http.StatusCreated || manager.completedStatus != http.StatusCreated || manager.completed == nil {
		t.Fatalf("first status=%d saved status=%d response=%+v", first.Code, manager.completedStatus, manager.completed)
	}
	stored, err := json.Marshal(idempotentHTTPResult{HTTPStatus: manager.completedStatus, Response: *manager.completed})
	if err != nil {
		t.Fatal(err)
	}
	manager.decision = idempotency.Decision{State: idempotency.StateCompleted, Response: stored}
	replayed := request("replay-request")
	var body Response
	if err := json.Unmarshal(replayed.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if replayed.Code != http.StatusCreated || called != 1 || body.RequestID != "replay-request" || !strings.Contains(replayed.Body.String(), "created-1") {
		t.Fatalf("replay status=%d calls=%d response=%+v", replayed.Code, called, body)
	}
}

func TestIdempotencyExecutionRejectsUnreplayableStatus(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		status int
	}{
		{name: "informational", status: http.StatusEarlyHints},
		{name: "redirect", status: http.StatusFound},
		{name: "not modified", status: http.StatusNotModified},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &fakeIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"}}
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(idempotency.WithContext(c.Request.Context(), "operation-1"))
				c.Next()
			}, IdempotencyExecution(manager, []string{"/test"}, slog.New(slog.NewTextHandler(io.Discard, nil))))
			router.POST("/test", func(c *gin.Context) {
				c.JSON(test.status, Response{Code: 0, Message: "unreplayable"})
			})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`)))
			if recorder.Code != http.StatusServiceUnavailable || !manager.aborted || manager.completed != nil || strings.Contains(recorder.Body.String(), "unreplayable") {
				t.Fatalf("status=%d aborted=%v completed=%+v body=%q", recorder.Code, manager.aborted, manager.completed, recorder.Body.String())
			}
		})
	}
}

func TestIdempotencyExecutionRejectsInvalidStoredStatus(t *testing.T) {
	t.Parallel()
	manager := &fakeIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateCompleted,
		Response: []byte(`{"http_status":302,"response":{"code":0,"message":"redirect"}}`)}}
	calls := 0
	router := idempotencyTestRouter(t, manager, &calls)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusServiceUnavailable || calls != 0 || strings.Contains(recorder.Body.String(), "redirect") {
		t.Fatalf("status=%d calls=%d body=%q", recorder.Code, calls, recorder.Body.String())
	}
}

func TestIdempotencyExecutionRejectsInvalidStoredEnvelope(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		decision idempotency.Decision
	}{
		{name: "missing success message", decision: idempotency.Decision{State: idempotency.StateCompleted, Response: []byte(`{"http_status":200,"response":{"code":0}}`)}},
		{name: "success status with error code", decision: idempotency.Decision{State: idempotency.StateCompleted, Response: []byte(`{"http_status":200,"response":{"code":10001,"message":"bad"}}`)}},
		{name: "bodyless success status", decision: idempotency.Decision{State: idempotency.StateCompleted, Response: []byte(`{"http_status":204,"response":{"code":0,"message":"success"}}`)}},
		{name: "failed status with success code", decision: idempotency.Decision{State: idempotency.StateFailed, Failure: idempotency.Failure{HTTPStatus: 400, Code: 0, Message: "bad"}}},
		{name: "failed status without message", decision: idempotency.Decision{State: idempotency.StateFailed, Failure: idempotency.Failure{HTTPStatus: 400, Code: 10001}}},
		{name: "retryable failed status", decision: idempotency.Decision{State: idempotency.StateFailed, Failure: idempotency.Failure{HTTPStatus: 429, Code: 10001, Message: "busy"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manager := &fakeIdempotencyManager{decision: test.decision}
			calls := 0
			router := idempotencyTestRouter(t, manager, &calls)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`)))
			if recorder.Code != http.StatusServiceUnavailable || calls != 0 {
				t.Fatalf("status=%d calls=%d body=%q", recorder.Code, calls, recorder.Body.String())
			}
		})
	}
}

func TestIdempotencyExecutionRejectsMalformedHandlerEnvelopeBeforePersistence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "null success", status: http.StatusOK, body: `null`},
		{name: "missing success message", status: http.StatusOK, body: `{"code":0}`},
		{name: "success status with error code", status: http.StatusOK, body: `{"code":10001,"message":"bad"}`},
		{name: "bodyless success status", status: http.StatusNoContent, body: `{"code":0,"message":"success"}`},
		{name: "failure status with success code", status: http.StatusBadRequest, body: `{"code":0,"message":"bad"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manager := &fakeIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"}}
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(idempotency.WithContext(c.Request.Context(), "operation-1"))
				c.Next()
			}, IdempotencyExecution(manager, []string{"/test"}, slog.New(slog.NewTextHandler(io.Discard, nil))))
			router.POST("/test", func(c *gin.Context) {
				c.Header("Set-Cookie", "session=private")
				c.Data(test.status, "application/json", []byte(test.body))
			})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`)))
			if recorder.Code != http.StatusServiceUnavailable || !manager.aborted || manager.completed != nil || manager.failed != nil || recorder.Header().Get("Set-Cookie") != "" {
				t.Fatalf("status=%d aborted=%v completed=%v failed=%v cookie=%q", recorder.Code, manager.aborted, manager.completed, manager.failed, recorder.Header().Get("Set-Cookie"))
			}
		})
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

func TestIdempotencyExecutionHidesResponseWhenFailureStateCannotPersist(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		status     int
		failureErr error
		abortErr   error
	}{
		{name: "nonretryable failure", status: http.StatusBadRequest, failureErr: errors.New("redis unavailable")},
		{name: "retryable failure", status: http.StatusServiceUnavailable, abortErr: errors.New("redis unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manager := &fakeIdempotencyManager{
				decision:   idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"},
				failureErr: test.failureErr, abortErr: test.abortErr,
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			router := gin.New()
			router.Use(RequestID(), func(c *gin.Context) {
				c.Request = c.Request.WithContext(idempotency.WithContext(c.Request.Context(), "operation-1"))
				c.Next()
			}, IdempotencyExecution(manager, []string{"/test"}, logger))
			router.POST("/test", func(c *gin.Context) {
				c.Header("Set-Cookie", "session=should-not-leak")
				c.JSON(test.status, Response{Code: 10001, Message: "original handler response", RequestID: requestID(c)})
			})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`)))
			var response Response
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusServiceUnavailable || response.Message == "original handler response" || response.RequestID == "" || recorder.Header().Get("Set-Cookie") != "" {
				t.Fatalf("status=%d response=%+v cookie=%q", recorder.Code, response, recorder.Header().Get("Set-Cookie"))
			}
		})
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
	if recorder.Code != http.StatusServiceUnavailable || !manager.aborted || manager.completed != nil || manager.failed != nil {
		t.Fatalf("status=%d aborted=%v completed=%+v failed=%+v", recorder.Code, manager.aborted, manager.completed, manager.failed)
	}
	if strings.Contains(recorder.Body.String(), strings.Repeat("x", 128)) {
		t.Fatal("oversized response body was sent to the client")
	}
}

func TestIdempotencyExecutionRejectsUnreplayableResponseOperations(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		operate func(*gin.Context) error
	}{
		{name: "flush", operate: func(c *gin.Context) error { c.Writer.Flush(); return nil }},
		{name: "hijack", operate: func(c *gin.Context) error {
			_, _, err := c.Writer.Hijack()
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manager := &fakeIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"}}
			router := gin.New()
			router.Use(RequestID(), func(c *gin.Context) {
				c.Request = c.Request.WithContext(idempotency.WithContext(c.Request.Context(), "operation-1"))
				c.Next()
			}, IdempotencyExecution(manager, []string{"/test"}, slog.New(slog.NewTextHandler(io.Discard, nil))))
			router.POST("/test", func(c *gin.Context) {
				if err := test.operate(c); test.name == "hijack" && !errors.Is(err, errIdempotencyHijack) {
					t.Errorf("Hijack() error = %v", err)
				}
				OK(c, "unreplayable")
			})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`)))
			if recorder.Code != http.StatusServiceUnavailable || !manager.aborted || strings.Contains(recorder.Body.String(), "unreplayable") {
				t.Fatalf("status=%d aborted=%v body=%s", recorder.Code, manager.aborted, recorder.Body.String())
			}
		})
	}
}

func TestIdempotencyExecutionRestoresWriterAndLeaseAfterPanic(t *testing.T) {
	t.Parallel()
	manager := &fakeIdempotencyManager{decision: idempotency.Decision{State: idempotency.StateAcquired, Owner: "owner-1"}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := gin.New()
	router.Use(RequestID(), Recovery(logger), func(c *gin.Context) {
		c.Request = c.Request.WithContext(idempotency.WithContext(c.Request.Context(), "operation-1"))
		c.Next()
	}, IdempotencyExecution(manager, []string{"/test"}, logger))
	router.POST("/test", func(c *gin.Context) {
		c.Header("Set-Cookie", "session=should-not-leak")
		panic("secret-value")
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusInternalServerError || !manager.leaseStopped || recorder.Header().Get("Set-Cookie") != "" || strings.Contains(recorder.Body.String(), "secret-value") {
		t.Fatalf("status=%d lease_stopped=%v cookie=%q body=%s", recorder.Code, manager.leaseStopped, recorder.Header().Get("Set-Cookie"), recorder.Body.String())
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
		{name: "oversized bearer is unauthorized", header: "Bearer " + strings.Repeat("x", 16*1024+1), status: http.StatusUnauthorized},
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

func TestDatabaseAuthenticationDistinguishesStateOutageFromRevocation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	jwtConfig, err := testutil.JWTConfig()
	if err != nil {
		t.Fatal(err)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service := auth.NewWithDatabase(config.Config{JWT: jwtConfig}, sqlx.NewDb(db, "sqlmock"))
	token, err := service.IssuePrincipal(platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := gin.New()
	router.Use(RequestID(), DatabaseAuthentication(service, logger, config.Config{}))
	router.POST("/test", func(c *gin.Context) { OK(c, nil) })
	for _, test := range []struct {
		name     string
		queryErr error
		status   int
		code     int
	}{
		{name: "database outage", queryErr: errors.New("database connection lost"), status: http.StatusServiceUnavailable, code: apperror.CodeDependencyUnavailable},
		{name: "dependency canceled", queryErr: context.Canceled, status: http.StatusServiceUnavailable, code: apperror.CodeDependencyUnavailable},
		{name: "dependency timed out", queryErr: context.DeadlineExceeded, status: http.StatusServiceUnavailable, code: apperror.CodeDependencyUnavailable},
		{name: "revoked session", status: http.StatusUnauthorized, code: apperror.CodeUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := mock.ExpectQuery(`SELECT c.must_change_password FROM identity_sessions`)
			if test.queryErr != nil {
				query.WillReturnError(test.queryErr)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"must_change_password"}))
			}
			request := httptest.NewRequest(http.MethodPost, "/test", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			var body Response
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != test.status || body.Code != test.code || body.RequestID == "" || strings.Contains(recorder.Body.String(), "database connection lost") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBearerVerificationErrorUsesCallerContext(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		ctx  func() context.Context
		err  error
		code int
	}{
		{name: "canceled caller", ctx: func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }, err: auth.ErrStateUnavailable, code: apperror.CodeRequestTimeout},
		{name: "expired caller", ctx: func() context.Context {
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			return ctx
		}, err: auth.ErrStateUnavailable, code: apperror.CodeRequestTimeout},
		{name: "dependency timed out", err: errors.Join(auth.ErrStateUnavailable, context.DeadlineExceeded), code: apperror.CodeDependencyUnavailable},
		{name: "database unavailable", err: auth.ErrStateUnavailable, code: apperror.CodeDependencyUnavailable},
		{name: "invalid token", err: errors.New("invalid signature"), code: apperror.CodeUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			if tt.ctx != nil {
				ctx = tt.ctx()
			}
			if got := bearerVerificationError(ctx, tt.err).Code; got != tt.code {
				t.Fatalf("code=%d, want %d", got, tt.code)
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

func TestRequireJSONChecksEmptyPOSTAndKeepsHealthProbeExceptions(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name        string
		path        string
		contentType string
		wantStatus  int
	}{
		{name: "empty API body without content type", path: "/api/v1/test", wantStatus: http.StatusUnsupportedMediaType},
		{name: "empty API body with plain text", path: "/api/v1/test", contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType},
		{name: "empty API body with JSON", path: "/api/v1/test", contentType: "application/json", wantStatus: http.StatusOK},
		{name: "upload multipart exception", path: "/api/v1/files/upload", contentType: "multipart/form-data; boundary=test", wantStatus: http.StatusOK},
		{name: "multipart on other route", path: "/api/v1/test", contentType: "multipart/form-data; boundary=test", wantStatus: http.StatusUnsupportedMediaType},
		{name: "liveness probe", path: "/live", wantStatus: http.StatusOK},
		{name: "readiness probe", path: "/ready", wantStatus: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			router := gin.New()
			router.Use(RequestID(), RequireJSON())
			router.POST(test.path, func(c *gin.Context) { OK(c, nil) })
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
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
		login     bool
		key       string
		status    int
		called    bool
	}{
		{name: "service-account login fails closed", dimension: "login", login: true, key: "key", status: http.StatusServiceUnavailable},
		{name: "user login fails closed", dimension: "user-login", login: true, key: "key", status: http.StatusServiceUnavailable},
		{name: "new login dimension fails closed", dimension: "future-signin", login: true, key: "key", status: http.StatusServiceUnavailable},
		{name: "service-account login without key fails closed", dimension: "login", login: true, status: http.StatusServiceUnavailable},
		{name: "user login without key fails closed", dimension: "user-login", login: true, status: http.StatusServiceUnavailable},
		{name: "ordinary API follows fail open", dimension: "api", key: "key", status: http.StatusOK, called: true},
		{name: "ordinary API without key remains available", dimension: "api", status: http.StatusOK, called: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			router := gin.New()
			keyFunc := func(*gin.Context) string { return test.key }
			middleware := RateLimit(limiter, rule, test.dimension, keyFunc, logger)
			if test.login {
				middleware = LoginRateLimit(limiter, rule, test.dimension, keyFunc, logger)
			}
			router.Use(RequestID(), middleware)
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
