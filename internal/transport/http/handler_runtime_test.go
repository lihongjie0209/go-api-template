package httptransport

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/health"
	"github.com/stretchr/testify/require"
)

func TestHandlerRuntimeStatusReturnsCurrentDependencyState(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	handler := NewHandler(
		health.New(nil, nil, config.Config{}),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/v1/platform/runtime/status", nil)

	handler.RuntimeStatus(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Code int                       `json:"code"`
		Body RuntimeStatusResponseBody `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Zero(t, response.Code)
	require.Equal(t, "up", response.Body.Liveness.Status)
	require.False(t, response.Body.Ready)
	require.Equal(t, "not_ready", response.Body.Readiness.Status)
	require.Equal(t, "disabled", response.Body.Readiness.Dependencies["database"].Status)
	require.Equal(t, "down", response.Body.Readiness.Dependencies["redis"].Status)
	require.NotEmpty(t, response.Body.Build.StartedAt)
}
