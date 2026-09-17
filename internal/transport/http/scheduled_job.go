package httptransport

import (
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/scheduler"
)

type ScheduledJobHandler struct {
	service  *scheduler.DefinitionService
	handlers *scheduler.HandlerRegistry
	logger   *slog.Logger
}

func NewScheduledJobHandler(service *scheduler.DefinitionService, handlers *scheduler.HandlerRegistry, logger *slog.Logger) *ScheduledJobHandler {
	return &ScheduledJobHandler{service: service, handlers: handlers, logger: logger}
}

type CreateScheduledJobRequest struct {
	Code           string          `json:"code" binding:"required,max=64"`
	Name           string          `json:"name" binding:"required,max=256"`
	Description    string          `json:"description" binding:"max=4096"`
	CronSpec       string          `json:"cron_spec" binding:"required,max=256"`
	Timezone       string          `json:"timezone" binding:"required,max=100"`
	Handler        string          `json:"handler" binding:"required,max=128"`
	TimeoutSeconds int64           `json:"timeout_seconds" binding:"required,gte=1,lte=3600"`
	LockTTLSeconds int64           `json:"lock_ttl_seconds" binding:"required,gte=1,lte=86400"`
	Status         string          `json:"status" binding:"required,oneof=active disabled"`
	Payload        json.RawMessage `json:"payload" swaggertype:"object"`
}

type ScheduledJobIDRequest struct {
	ID string `json:"id" binding:"required,max=128"`
}
type DeleteScheduledJobRequest struct {
	ID      string `json:"id" binding:"required,max=128"`
	Version int64  `json:"version" binding:"required,gt=0"`
}
type UpdateScheduledJobRequest struct {
	ID             string          `json:"id" binding:"required,max=128"`
	Name           string          `json:"name" binding:"required,max=256"`
	Description    string          `json:"description" binding:"max=4096"`
	CronSpec       string          `json:"cron_spec" binding:"required,max=256"`
	Timezone       string          `json:"timezone" binding:"required,max=100"`
	Handler        string          `json:"handler" binding:"required,max=128"`
	TimeoutSeconds int64           `json:"timeout_seconds" binding:"required,gte=1,lte=3600"`
	LockTTLSeconds int64           `json:"lock_ttl_seconds" binding:"required,gte=1,lte=86400"`
	Status         string          `json:"status" binding:"required,oneof=active disabled"`
	Payload        json.RawMessage `json:"payload" swaggertype:"object"`
	Version        int64           `json:"version" binding:"required,gt=0"`
}
type ScheduledJobPageRequest struct {
	pagination.Request
	IDs           []string          `json:"ids" binding:"max=200,dive,required,max=128"`
	Codes         []string          `json:"codes" binding:"max=200,dive,required,max=64"`
	Statuses      []string          `json:"statuses" binding:"max=2,dive,oneof=active disabled"`
	Handlers      []string          `json:"handlers" binding:"max=200,dive,required,max=128"`
	CreatedAtFrom *time.Time        `json:"created_at_from"`
	CreatedAtTo   *time.Time        `json:"created_at_to"`
	Sort          []pagination.Sort `json:"sort" binding:"max=3,dive"`
}
type ScheduledJobRunPageRequest struct {
	pagination.Request
	ScheduledJobID string            `json:"scheduled_job_id" binding:"required,max=128"`
	Statuses       []string          `json:"statuses" binding:"max=3,dive,oneof=success error skipped"`
	TriggerSources []string          `json:"trigger_sources" binding:"max=2,dive,oneof=cron manual"`
	StartedAtFrom  *time.Time        `json:"started_at_from"`
	StartedAtTo    *time.Time        `json:"started_at_to"`
	Sort           []pagination.Sort `json:"sort" binding:"max=3,dive"`
}

// CreateScheduledJob godoc
// @Summary Create a scheduled job definition
// @Tags scheduled-jobs
// @Security Bearer
// @Param request body CreateScheduledJobRequest true "Scheduled job"
// @Success 200 {object} Response{body=scheduler.Definition}
// @Router /api/v1/scheduled-jobs/create [post]
func (h *ScheduledJobHandler) Create(c *gin.Context) {
	var request CreateScheduledJobRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.Create(c.Request.Context(), scheduler.DefinitionInput{Code: request.Code, Name: request.Name, Description: request.Description, CronSpec: request.CronSpec, Timezone: request.Timezone, Handler: request.Handler, TimeoutSeconds: request.TimeoutSeconds, LockTTLSeconds: request.LockTTLSeconds, Status: request.Status, Payload: request.Payload})
	h.respond(c, result, err)
}

// GetScheduledJob godoc
// @Summary Get a scheduled job definition
// @Tags scheduled-jobs
// @Security Bearer
// @Param request body ScheduledJobIDRequest true "Scheduled job"
// @Success 200 {object} Response{body=scheduler.Definition}
// @Router /api/v1/scheduled-jobs/get [post]
func (h *ScheduledJobHandler) Get(c *gin.Context) {
	var request ScheduledJobIDRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.Get(c.Request.Context(), request.ID)
	h.respond(c, result, err)
}

// PageScheduledJobs godoc
// @Summary Page scheduled job definitions
// @Tags scheduled-jobs
// @Security Bearer
// @Param request body ScheduledJobPageRequest true "Filters"
// @Success 200 {object} Response{body=scheduler.DefinitionPage}
// @Router /api/v1/scheduled-jobs/page [post]
func (h *ScheduledJobHandler) Page(c *gin.Context) {
	var request ScheduledJobPageRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.Page(c.Request.Context(), scheduler.DefinitionPageInput{Request: request.Request, IDs: request.IDs, Codes: request.Codes, Statuses: request.Statuses, Handlers: request.Handlers, CreatedAtFrom: request.CreatedAtFrom, CreatedAtTo: request.CreatedAtTo, Sort: request.Sort})
	h.respond(c, result, err)
}

// UpdateScheduledJob godoc
// @Summary Update a scheduled job definition
// @Tags scheduled-jobs
// @Security Bearer
// @Param request body UpdateScheduledJobRequest true "Scheduled job"
// @Success 200 {object} Response{body=scheduler.Definition}
// @Router /api/v1/scheduled-jobs/update [post]
func (h *ScheduledJobHandler) Update(c *gin.Context) {
	var request UpdateScheduledJobRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.Update(c.Request.Context(), scheduler.UpdateDefinitionInput{ID: request.ID, Name: request.Name, Description: request.Description, CronSpec: request.CronSpec, Timezone: request.Timezone, Handler: request.Handler, TimeoutSeconds: request.TimeoutSeconds, LockTTLSeconds: request.LockTTLSeconds, Status: request.Status, Payload: request.Payload, Version: request.Version})
	h.respond(c, result, err)
}

// DeleteScheduledJob godoc
// @Summary Delete a scheduled job definition
// @Tags scheduled-jobs
// @Security Bearer
// @Param request body DeleteScheduledJobRequest true "Scheduled job"
// @Success 200 {object} Response
// @Router /api/v1/scheduled-jobs/delete [post]
func (h *ScheduledJobHandler) Delete(c *gin.Context) {
	var request DeleteScheduledJobRequest
	if !h.bind(c, &request) {
		return
	}
	h.respond(c, gin.H{}, h.service.Delete(c.Request.Context(), request.ID, request.Version))
}

// ListScheduledJobHandlers godoc
// @Summary List executable scheduled job handlers registered by code
// @Tags scheduled-jobs
// @Security Bearer
// @Param request body object true "Empty JSON object"
// @Success 200 {object} Response{body=[]scheduler.HandlerDefinition}
// @Router /api/v1/scheduled-jobs/handlers/list [post]
func (h *ScheduledJobHandler) ListHandlers(c *gin.Context) {
	var request struct{}
	if !h.bind(c, &request) {
		return
	}
	OK(c, h.handlers.Definitions())
}

// TriggerScheduledJob godoc
// @Summary Trigger one scheduled job immediately
// @Tags scheduled-jobs
// @Security Bearer
// @Param request body ScheduledJobIDRequest true "Scheduled job"
// @Success 200 {object} Response
// @Router /api/v1/scheduled-jobs/trigger [post]
func (h *ScheduledJobHandler) Trigger(c *gin.Context) {
	var request ScheduledJobIDRequest
	if !h.bind(c, &request) {
		return
	}
	h.respond(c, gin.H{}, h.service.Trigger(c.Request.Context(), request.ID))
}

// GetScheduledJobRun godoc
// @Summary Get a scheduled job execution record
// @Tags scheduled-jobs
// @Security Bearer
// @Param request body ScheduledJobIDRequest true "Run ID"
// @Success 200 {object} Response{body=scheduler.Run}
// @Router /api/v1/scheduled-job-runs/get [post]
func (h *ScheduledJobHandler) GetRun(c *gin.Context) {
	var request ScheduledJobIDRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.GetRun(c.Request.Context(), request.ID)
	h.respond(c, result, err)
}

// PageScheduledJobRuns godoc
// @Summary Page one scheduled job's execution records
// @Tags scheduled-jobs
// @Security Bearer
// @Param request body ScheduledJobRunPageRequest true "Run filters"
// @Success 200 {object} Response{body=scheduler.RunPage}
// @Router /api/v1/scheduled-job-runs/page [post]
func (h *ScheduledJobHandler) PageRuns(c *gin.Context) {
	var request ScheduledJobRunPageRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.PageRuns(c.Request.Context(), scheduler.RunPageInput{Request: request.Request, ScheduledJobID: request.ScheduledJobID, Statuses: request.Statuses, TriggerSources: request.TriggerSources, StartedAtFrom: request.StartedAtFrom, StartedAtTo: request.StartedAtTo, Sort: request.Sort})
	h.respond(c, result, err)
}

func (h *ScheduledJobHandler) bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid scheduled job request", err))
		return false
	}
	return true
}
func (h *ScheduledJobHandler) respond(c *gin.Context, body any, err error) {
	switch {
	case err == nil:
		OK(c, body)
	case errors.Is(err, scheduler.ErrInvalidDefinition):
		Fail(c, h.logger, apperror.Invalid("invalid scheduled job definition", err))
	case errors.Is(err, scheduler.ErrDefinitionNotFound):
		Fail(c, h.logger, apperror.NotFound("scheduled job definition not found"))
	case errors.Is(err, scheduler.ErrDefinitionConflict):
		Fail(c, h.logger, apperror.Conflict("scheduled job definition conflict", err))
	case errors.Is(err, scheduler.ErrSkipped):
		Fail(c, h.logger, apperror.Conflict("scheduled job is already running", err))
	default:
		Fail(c, h.logger, apperror.Internal(err))
	}
}
