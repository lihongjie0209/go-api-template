package httptransport

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/observability"
)

func TestGlobalMiddlewareKeepsInvalidIdempotencyKeyObservableAndProtected(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	cfg := config.Config{
		App:           config.App{Name: "test-service"},
		Observability: config.Observability{MetricsEnabled: true},
		HTTP: config.HTTP{
			RequestTimeout: time.Second,
			MaxBodyBytes:   1 << 20,
			CORS: config.CORS{
				Enabled: true, AllowedOrigins: []string{"https://app.example"},
			},
		},
	}
	metrics := observability.NewMetrics(cfg, nil, nil)
	router := gin.New()
	router.Use(globalHTTPMiddleware(cfg, metrics, logger)...)
	called := false
	router.POST("/api/v1/test", func(c *gin.Context) {
		called = true
		OK(c, nil)
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/test", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://app.example")
	request.Header.Set("Idempotency-Key", "bad key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || called {
		t.Fatalf("status=%d handler_called=%v body=%q", response.Code, called, response.Body.String())
	}
	if response.Header().Get("X-Request-ID") == "" || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatalf("required response headers missing: %#v", response.Header())
	}
	if !strings.Contains(logs.String(), `"msg":"http request"`) || !strings.Contains(logs.String(), `"status":400`) {
		t.Fatalf("invalid-key response missing access log: %s", logs.String())
	}
	metricResponse := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(metricResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metricResponse.Body.String(), `http_requests_total{method="POST",route="/api/v1/test",status="400"} 1`) {
		t.Fatalf("invalid-key response missing HTTP metric: %s", metricResponse.Body.String())
	}
}
