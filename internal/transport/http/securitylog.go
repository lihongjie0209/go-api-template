package httptransport

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type SecurityLogHandler struct {
	service securityLogService
	logger  *slog.Logger
}

type securityLogService interface {
	Enabled() bool
	Get(ctx context.Context, id string) (securitylog.Record, error)
	Page(ctx context.Context, input securitylog.PageInput) (securitylog.Page, error)
	SystemGet(ctx context.Context, id string) (securitylog.Record, error)
	SystemPage(ctx context.Context, input securitylog.PageInput) (securitylog.Page, error)
	Record(ctx context.Context, entry securitylog.Entry) error
}

func NewSecurityLogHandler(service *securitylog.Service, logger *slog.Logger) *SecurityLogHandler {
	return &SecurityLogHandler{service: service, logger: logger}
}

type SecurityLogIDRequest struct {
	ID string `json:"id" binding:"required"`
}

type SecurityLogPageRequest struct {
	pagination.Request
	Keyword        string                  `json:"keyword"`
	IDs            []string                `json:"ids"`
	TenantIDs      []string                `json:"tenant_ids"`
	ActorIDs       []string                `json:"actor_ids"`
	SubjectIDs     []string                `json:"subject_ids"`
	SubjectTypes   []string                `json:"subject_types"`
	EventTypes     []securitylog.EventType `json:"event_types"`
	SessionIDs     []string                `json:"session_ids"`
	RequestIDs     []string                `json:"request_ids"`
	TraceIDs       []string                `json:"trace_ids" binding:"max=200"`
	ClientIPs      []string                `json:"client_ips"`
	Identifier     string                  `json:"identifier"`
	Succeeded      *bool                   `json:"succeeded"`
	OccurredAtFrom *time.Time              `json:"occurred_at_from"`
	OccurredAtTo   *time.Time              `json:"occurred_at_to"`
}

// Get godoc
// @Summary Get a security log within the caller's tenant scope
// @Tags security-logs
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body SecurityLogIDRequest true "Security log"
// @Success 200 {object} Response{body=securitylog.Record}
// @Failure 400 {object} Response
// @Failure 404 {object} Response
// @Failure 503 {object} Response
// @Router /api/v1/security-logs/get [post]
func (h *SecurityLogHandler) Get(c *gin.Context) {
	if !h.service.Enabled() {
		Fail(c, h.logger, apperror.Unavailable("security log is unavailable", nil))
		return
	}
	var request SecurityLogIDRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid security log request", err))
		return
	}
	record, err := h.service.Get(c.Request.Context(), request.ID)
	if err != nil {
		h.fail(c, err)
		return
	}
	if err := h.recordAccess(c, "get", request.ID, 1); err != nil {
		Fail(c, h.logger, apperror.Unavailable("record security log access", err))
		return
	}
	OK(c, record)
}

// Page godoc
// @Summary Page security logs within the caller's tenant scope
// @Tags security-logs
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body SecurityLogPageRequest true "Filters"
// @Success 200 {object} Response{body=securitylog.Page}
// @Failure 400 {object} Response
// @Failure 503 {object} Response
// @Router /api/v1/security-logs/page [post]
func (h *SecurityLogHandler) Page(c *gin.Context) {
	if !h.service.Enabled() {
		Fail(c, h.logger, apperror.Unavailable("security log is unavailable", nil))
		return
	}
	var request SecurityLogPageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid security log filters", err))
		return
	}
	page, err := h.service.Page(c.Request.Context(), request.input())
	if err != nil {
		h.fail(c, err)
		return
	}
	if err := h.recordAccess(c, "page", "", len(page.Items)); err != nil {
		Fail(c, h.logger, apperror.Unavailable("record security log access", err))
		return
	}
	OK(c, page)
}

func (request SecurityLogPageRequest) input() securitylog.PageInput {
	return securitylog.PageInput{
		Request: request.Request, Keyword: request.Keyword, IDs: request.IDs, TenantIDs: request.TenantIDs,
		ActorIDs: request.ActorIDs, SubjectIDs: request.SubjectIDs, SubjectTypes: request.SubjectTypes,
		EventTypes: request.EventTypes, SessionIDs: request.SessionIDs, RequestIDs: request.RequestIDs, TraceIDs: request.TraceIDs,
		ClientIPs: request.ClientIPs, Identifier: request.Identifier, Succeeded: request.Succeeded,
		OccurredAtFrom: request.OccurredAtFrom, OccurredAtTo: request.OccurredAtTo,
	}
}

// SystemGet godoc
// @Summary Get a platform system security log
// @Tags security-logs
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body SecurityLogIDRequest true "System security log"
// @Success 200 {object} Response{body=securitylog.Record}
// @Failure 400 {object} Response
// @Failure 403 {object} Response
// @Failure 404 {object} Response
// @Failure 503 {object} Response
// @Router /api/v1/platform/security-logs/get [post]
func (h *SecurityLogHandler) SystemGet(c *gin.Context) {
	if !h.service.Enabled() {
		Fail(c, h.logger, apperror.Unavailable("security log is unavailable", nil))
		return
	}
	var request SecurityLogIDRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid system security log request", err))
		return
	}
	record, err := h.service.SystemGet(c.Request.Context(), request.ID)
	if err != nil {
		h.fail(c, err)
		return
	}
	if err := h.recordAccess(c, "system_get", request.ID, 1); err != nil {
		Fail(c, h.logger, apperror.Unavailable("record security log access", err))
		return
	}
	OK(c, record)
}

// SystemPage godoc
// @Summary Page platform system security logs in a bounded time window
// @Tags security-logs
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body SecurityLogPageRequest true "System log filters; time range required, maximum 31 days"
// @Success 200 {object} Response{body=securitylog.Page}
// @Failure 400 {object} Response
// @Failure 403 {object} Response
// @Failure 503 {object} Response
// @Router /api/v1/platform/security-logs/page [post]
func (h *SecurityLogHandler) SystemPage(c *gin.Context) {
	if !h.service.Enabled() {
		Fail(c, h.logger, apperror.Unavailable("security log is unavailable", nil))
		return
	}
	var request SecurityLogPageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid system security log filters", err))
		return
	}
	page, err := h.service.SystemPage(c.Request.Context(), request.input())
	if err != nil {
		h.fail(c, err)
		return
	}
	if err := h.recordAccess(c, "system_page", "", len(page.Items)); err != nil {
		Fail(c, h.logger, apperror.Unavailable("record security log access", err))
		return
	}
	OK(c, page)
}

func (h *SecurityLogHandler) recordAccess(c *gin.Context, operation, resourceID string, resultCount int) error {
	return h.service.Record(c.Request.Context(), securitylog.Entry{
		EventType:   securitylog.EventSecurityLogAccess,
		SubjectID:   resourceID,
		SubjectType: "security_log",
		Succeeded:   true,
		ClientIP:    c.ClientIP(),
		UserAgent:   c.Request.UserAgent(),
		Metadata:    map[string]any{"operation": operation, "result_count": resultCount},
	})
}

func (h *SecurityLogHandler) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, securitylog.ErrTenantRequired):
		Fail(c, h.logger, apperror.Forbidden("active tenant is required"))
	case errors.Is(err, securitylog.ErrPlatformContextRequired):
		Fail(c, h.logger, apperror.Forbidden("platform context is required"))
	case errors.Is(err, securitylog.ErrInvalidEntry):
		Fail(c, h.logger, apperror.Invalid("invalid security log request", err))
	case errors.Is(err, securitylog.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("security log not found"))
	case errors.Is(err, platformprincipal.ErrMissing):
		Fail(c, h.logger, apperror.Unauthorized("authenticated principal is required"))
	default:
		Fail(c, h.logger, err)
	}
}
