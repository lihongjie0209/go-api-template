package httptransport

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	apphealth "github.com/lihongjie0209/go-api-template/internal/health"
	"github.com/redis/go-redis/v9"
)

func TestHealthHandlersUseUnifiedEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	healthService := apphealth.New(nil, client, config.Config{Health: config.Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second}})
	handler := NewHandler(healthService, auth.New(config.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := gin.New()
	router.Use(RequestID())
	router.GET("/live", handler.Live)
	router.POST("/ready", handler.Ready)

	for _, test := range []struct {
		name, method, path string
	}{
		{name: "liveness GET", method: http.MethodGet, path: "/live"},
		{name: "readiness POST", method: http.MethodPost, path: "/ready"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, nil)
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var envelope struct {
				Code      int              `json:"code"`
				Message   string           `json:"message"`
				Body      apphealth.Status `json:"body"`
				RequestID string           `json:"request_id"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Code != apperror.CodeOK || envelope.Message != "success" || envelope.RequestID == "" {
				t.Fatalf("envelope = %+v", envelope)
			}
		})
	}
}

func TestReadyHandlerReturnsDependencyUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	healthService := apphealth.New(nil, nil, config.Config{Health: config.Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second}})
	handler := NewHandler(healthService, auth.New(config.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := gin.New()
	router.Use(RequestID())
	router.GET("/ready", handler.Ready)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Code      int              `json:"code"`
		Body      apphealth.Status `json:"body"`
		RequestID string           `json:"request_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != apperror.CodeDependencyUnavailable || envelope.Body.Status != "not_ready" || envelope.Body.Dependencies["redis"].Status != "down" || envelope.RequestID == "" {
		t.Fatalf("envelope = %+v", envelope)
	}
}
