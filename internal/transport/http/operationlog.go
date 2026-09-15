package httptransport

import (
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type OperationLogHandler struct {
	service  *operationlog.Service
	recorder operationlog.Recorder
	logger   *slog.Logger
}

func NewOperationLogHandler(service *operationlog.Service, logger *slog.Logger) *OperationLogHandler {
	return &OperationLogHandler{service: service, recorder: service, logger: logger}
}

type OperationLogIDRequest struct {
	ID string `json:"id" binding:"required"`
}

type OperationLogPageRequest struct {
	pagination.Request
	Keyword        string     `json:"keyword"`
	IDs            []string   `json:"ids"`
	TenantIDs      []string   `json:"tenant_ids"`
	ActorIDs       []string   `json:"actor_ids"`
	ApplicationIDs []string   `json:"application_ids"`
	Operations     []string   `json:"operations"`
	ResourceTypes  []string   `json:"resource_types"`
	ResourceIDs    []string   `json:"resource_ids"`
	Sources        []string   `json:"sources"`
	Protocols      []string   `json:"protocols"`
	RequestIDs     []string   `json:"request_ids"`
	Succeeded      *bool      `json:"succeeded"`
	OccurredAtFrom *time.Time `json:"occurred_at_from"`
	OccurredAtTo   *time.Time `json:"occurred_at_to"`
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

// Get godoc
// @Summary Get an operation log within the caller's tenant scope
// @Tags operation-logs
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body OperationLogIDRequest true "Operation log"
// @Success 200 {object} Response{body=operationlog.Record}
// @Failure 400 {object} Response
// @Failure 404 {object} Response
// @Router /api/v1/operation-logs/get [post]
func (h *OperationLogHandler) Get(c *gin.Context) {
	var request OperationLogIDRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid operation log request", err))
		return
	}
	record, err := h.service.Get(c.Request.Context(), request.ID)
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, record)
}

// Page godoc
// @Summary Page operation logs within the caller's tenant scope
// @Tags operation-logs
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body OperationLogPageRequest true "Filters"
// @Success 200 {object} Response{body=operationlog.Page}
// @Failure 400 {object} Response
// @Router /api/v1/operation-logs/page [post]
func (h *OperationLogHandler) Page(c *gin.Context) {
	var request OperationLogPageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid operation log filters", err))
		return
	}
	page, err := h.service.Page(c.Request.Context(), operationlog.PageInput{
		Request: request.Request, Keyword: request.Keyword, IDs: request.IDs, TenantIDs: request.TenantIDs,
		ActorIDs: request.ActorIDs, ApplicationIDs: request.ApplicationIDs, Operations: request.Operations,
		ResourceTypes: request.ResourceTypes, ResourceIDs: request.ResourceIDs, Sources: request.Sources,
		Protocols: request.Protocols, RequestIDs: request.RequestIDs, Succeeded: request.Succeeded,
		OccurredAtFrom: request.OccurredAtFrom, OccurredAtTo: request.OccurredAtTo,
	})
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, page)
}

func (h *OperationLogHandler) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, operationlog.ErrInvalidEntry):
		Fail(c, h.logger, apperror.Invalid("invalid operation log request", err))
	case errors.Is(err, operationlog.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("operation log not found"))
	case errors.Is(err, platformprincipal.ErrMissing):
		Fail(c, h.logger, apperror.Unauthorized("authenticated principal is required"))
	default:
		Fail(c, h.logger, err)
	}
}
