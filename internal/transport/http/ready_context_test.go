package httptransport

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/health"
)

func TestReadyDoesNotReportDependencyFailureAfterCallerContextEnds(t *testing.T) {
	t.Parallel()
	service := health.New(nil, nil, config.Config{})
	handler := NewHandler(service, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := gin.New()
	router.POST("/ready", handler.Ready)
	for _, test := range []struct {
		name       string
		context    func() (context.Context, context.CancelFunc)
		wantStatus int
		wantBody   bool
	}{
		{name: "caller canceled", context: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}, wantStatus: http.StatusOK},
		{name: "deadline elapsed", context: func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}, wantStatus: http.StatusGatewayTimeout, wantBody: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.context()
			defer cancel()
			request := httptest.NewRequest(http.MethodPost, "/ready", nil).WithContext(ctx)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus || (response.Body.Len() > 0) != test.wantBody {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
}
