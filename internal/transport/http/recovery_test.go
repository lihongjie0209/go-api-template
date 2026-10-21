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
)

func TestHTTPRecoveryDoesNotLogPanicOrRequestSecrets(t *testing.T) {
	var applicationLog bytes.Buffer
	var frameworkLog bytes.Buffer
	previous := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &frameworkLog
	t.Cleanup(func() { gin.DefaultErrorWriter = previous })
	router := gin.New()
	router.Use(RequestID(), Recovery(slog.New(slog.NewJSONHandler(&applicationLog, nil))))
	router.POST("/test", func(*gin.Context) { panic(errors.New("credential=secret-value")) })
	request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`))
	request.Header.Set("X-Private-Token", "request-secret-value")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	for _, output := range []string{applicationLog.String(), frameworkLog.String(), response.Body.String()} {
		if strings.Contains(output, "secret-value") {
			t.Fatalf("HTTP recovery leaked panic or request details: %s", output)
		}
	}
	if !strings.Contains(applicationLog.String(), "http_handler_panic") {
		t.Fatalf("HTTP recovery did not log a bounded panic reason: %s", applicationLog.String())
	}
}
