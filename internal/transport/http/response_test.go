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

func TestResponseHelpersHonorExpiredRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, test := range []struct {
		name  string
		write func(*gin.Context)
	}{
		{name: "success", write: func(c *gin.Context) { OK(c, map[string]any{"stale": true}) }},
		{name: "failure", write: func(c *gin.Context) {
			Fail(c, logger, apperror.Unavailable("dependency unavailable", errors.New("private detail")))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.Use(RequestID(), Timeout(time.Millisecond, logger))
			router.POST("/test", func(c *gin.Context) {
				<-c.Request.Context().Done()
				test.write(c)
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/test", nil))
			var body Response
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusGatewayTimeout || body.Code != apperror.CodeRequestTimeout || body.RequestID == "" || strings.Contains(response.Body.String(), "stale") {
				t.Fatalf("late %s response = %d %s", test.name, response.Code, response.Body.String())
			}
		})
	}
}

func TestResponseHelpersSkipCanceledClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, write := range []func(*gin.Context){
		func(c *gin.Context) { OK(c, "late") },
		func(c *gin.Context) { Fail(c, logger, apperror.Internal(errors.New("late"))) },
	} {
		router := gin.New()
		router.Use(RequestID())
		router.POST("/test", write)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		request := httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Body.Len() != 0 {
			t.Fatalf("canceled client received response: %s", response.Body.String())
		}
	}
}
