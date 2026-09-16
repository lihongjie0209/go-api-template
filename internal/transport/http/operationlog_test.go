package httptransport

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type missingPrincipalOperationRecorder struct{}

func (missingPrincipalOperationRecorder) Enabled() bool { return true }
func (missingPrincipalOperationRecorder) Record(context.Context, operationlog.Entry) error {
	return platformprincipal.ErrMissing
}

func TestRecordFrontendRequiresAuthenticatedPrincipal(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &OperationLogHandler{recorder: missingPrincipalOperationRecorder{}, logger: logger}
	router := gin.New()
	router.Use(RequestID())
	router.POST("/api/v1/operation-logs/frontend/record", handler.RecordFrontend)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/operation-logs/frontend/record", strings.NewReader(`{"event_type":"menu_view","event_name":"users","succeeded":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}
