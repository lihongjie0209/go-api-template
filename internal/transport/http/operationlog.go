package httptransport

import (
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
)

type OperationLogHandler struct {
	recorder operationlog.Recorder
	logger   *slog.Logger
}

func NewOperationLogHandler(recorder operationlog.Recorder, logger *slog.Logger) *OperationLogHandler {
	return &OperationLogHandler{recorder: recorder, logger: logger}
}

type FrontendEventRequest struct {
	EventType     string         `json:"event_type" binding:"required,oneof=menu_view button_click"`
	EventName     string         `json:"event_name" binding:"required,max=256"`
	ApplicationID string         `json:"application_id" binding:"omitempty,max=128"`
	ResourceID    string         `json:"resource_id" binding:"omitempty,max=256"`
	PageRoute     string         `json:"page_route" binding:"omitempty,max=1024"`
	DurationMS    int64          `json:"duration_ms" binding:"omitempty,gte=0,lte=86400000"`
	Succeeded     *bool          `json:"succeeded" binding:"required"`
	ErrorCode     string         `json:"error_code" binding:"omitempty,max=128"`
	ErrorMessage  string         `json:"error_message" binding:"omitempty,max=2048"`
	Extension     map[string]any `json:"extension" binding:"omitempty"`
}

// RecordFrontendEvent godoc
// @Summary Enqueue a frontend menu or button event
// @Tags operation-logs
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body FrontendEventRequest true "Frontend event"
// @Success 200 {object} Response
// @Failure 400 {object} Response
// @Failure 503 {object} Response
// @Router /api/v1/operation-logs/frontend/record [post]
func (h *OperationLogHandler) RecordFrontend(c *gin.Context) {
	if !h.recorder.Enabled() {
		Fail(c, h.logger, apperror.Unavailable("operation log is unavailable", nil))
		return
	}
	var request FrontendEventRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid frontend event", err))
		return
	}
	entry := operationlog.Entry{
		Operation:     "frontend." + request.EventType + "." + request.EventName,
		ResourceType:  request.EventType,
		ResourceID:    request.ResourceID,
		ApplicationID: request.ApplicationID,
		Source:        "frontend",
		Protocol:      "http",
		Method:        c.Request.Method,
		Route:         request.PageRoute,
		Request:       map[string]any{"event_type": request.EventType, "event_name": request.EventName, "page_route": request.PageRoute},
		Duration:      time.Duration(request.DurationMS) * time.Millisecond,
		Succeeded:     *request.Succeeded,
		ErrorCode:     request.ErrorCode,
		ErrorMessage:  request.ErrorMessage,
		ClientIP:      c.ClientIP(),
		UserAgent:     c.Request.UserAgent(),
		Extension:     request.Extension,
	}
	if err := h.recorder.Record(c.Request.Context(), entry); err != nil {
		if errors.Is(err, operationlog.ErrInvalidEntry) {
			Fail(c, h.logger, apperror.Invalid("invalid frontend event", err))
			return
		}
		Fail(c, h.logger, apperror.Unavailable("enqueue operation log", err))
		return
	}
	OK(c, gin.H{"accepted": true})
}
