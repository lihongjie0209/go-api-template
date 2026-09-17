package httptransport

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/scheduler"
	"github.com/stretchr/testify/require"
)

func TestScheduledJobHandlerListHandlersReturnsRegisteredMetadata(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewScheduledJobHandler(nil, scheduler.NewHandlerRegistry(logger), logger)
	router := gin.New()
	router.Use(RequestID())
	router.POST("/api/v1/scheduled-jobs/handlers/list", handler.ListHandlers)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/scheduled-jobs/handlers/list", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var envelope struct {
		Code int                           `json:"code"`
		Body []scheduler.HandlerDefinition `json:"body"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
	require.Equal(t, apperror.CodeOK, envelope.Code)
	require.Equal(t, []scheduler.HandlerDefinition{{
		Key:         "system.sample",
		Name:        "系统示例任务",
		Description: "记录一条带链路上下文的示例日志",
	}}, envelope.Body)
}

func TestScheduledJobHandlerRejectsInvalidTriggerRequest(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewScheduledJobHandler(nil, scheduler.NewHandlerRegistry(logger), logger)
	router := gin.New()
	router.Use(RequestID())
	router.POST("/api/v1/scheduled-jobs/trigger", handler.Trigger)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/scheduled-jobs/trigger", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	var envelope Response
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
	require.Equal(t, apperror.CodeInvalidArgument, envelope.Code)
	require.NotEmpty(t, envelope.RequestID)
}
