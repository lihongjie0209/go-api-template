package httptransport

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
)

func TestFailDoesNotLogInternalErrorDetails(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	router := gin.New()
	router.Use(RequestID())
	router.POST("/test", func(c *gin.Context) {
		Fail(c, logger, apperror.Unavailable("dependency unavailable", errors.New("redis password=secret-value")))
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(output.String(), "secret-value") || !strings.Contains(output.String(), "http_request_failed") {
		t.Fatalf("request failure log leaked dependency detail or omitted safe code: %s", output.String())
	}
	if strings.Contains(response.Body.String(), "secret-value") {
		t.Fatalf("response leaked dependency detail: %s", response.Body.String())
	}
}
