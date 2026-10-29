package httptransport

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"go.opentelemetry.io/otel/trace"
)

var errHTTPRequestFailed = errors.New("http_request_failed")

type Response struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Body      any    `json:"body"`
	RequestID string `json:"request_id,omitempty"`
}

func OK(c *gin.Context, body any) {
	if responseContextEnded(c) {
		return
	}
	c.JSON(http.StatusOK, Response{Code: apperror.CodeOK, Message: "success", Body: body, RequestID: requestID(c)})
}
func Fail(c *gin.Context, logger *slog.Logger, err error) {
	if responseContextEnded(c) {
		return
	}
	var appErr *apperror.Error
	if !errors.As(err, &appErr) {
		appErr = apperror.Internal(err)
	}
	if appErr.HTTPStatus >= 500 {
		span := trace.SpanFromContext(c.Request.Context()).SpanContext()
		logger.ErrorContext(c.Request.Context(), "request failed", "error", observability.SafeError(appErr.Err, errHTTPRequestFailed), "request_id", requestID(c), "trace_id", span.TraceID().String(), "span_id", span.SpanID().String())
	}
	c.AbortWithStatusJSON(appErr.HTTPStatus, Response{Code: appErr.Code, Message: appErr.Message, Body: nil, RequestID: requestID(c)})
}

func responseContextEnded(c *gin.Context) bool {
	switch c.Request.Context().Err() {
	case context.DeadlineExceeded:
		if !c.Writer.Written() {
			timeout := apperror.RequestTimeout()
			c.AbortWithStatusJSON(timeout.HTTPStatus, Response{Code: timeout.Code, Message: timeout.Message, Body: nil, RequestID: requestID(c)})
		}
		return true
	case context.Canceled:
		return true
	default:
		return false
	}
}
