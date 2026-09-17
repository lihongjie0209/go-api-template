package httptransport

import (
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/application"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
)

type ApplicationHandler struct {
	service *application.Service
	logger  *slog.Logger
}

func NewApplicationHandler(service *application.Service, logger *slog.Logger) *ApplicationHandler {
	return &ApplicationHandler{service: service, logger: logger}
}

type CreateApplicationRequest struct {
	Code        string          `json:"code" binding:"required,max=64"`
	Name        string          `json:"name" binding:"required,max=256"`
	Description string          `json:"description" binding:"max=4096"`
	Icon        string          `json:"icon" binding:"max=256"`
	HomePath    string          `json:"home_path" binding:"max=2048"`
	Status      string          `json:"status" binding:"required,oneof=active disabled"`
	SortOrder   int64           `json:"sort_order" binding:"gte=-1000000000,lte=1000000000"`
	Metadata    json.RawMessage `json:"metadata" swaggertype:"object"`
}
type ApplicationIDRequest struct {
	ID string `json:"id" binding:"required,max=128"`
}
type ApplicationPageRequest struct {
	pagination.Request
	IDs           []string          `json:"ids" binding:"max=200,dive,required,max=128"`
	Codes         []string          `json:"codes" binding:"max=200,dive,required,max=64"`
	Statuses      []string          `json:"statuses" binding:"max=2,dive,oneof=active disabled"`
	CreatedAtFrom *time.Time        `json:"created_at_from"`
	CreatedAtTo   *time.Time        `json:"created_at_to"`
	Sort          []pagination.Sort `json:"sort" binding:"max=3,dive"`
}
type UpdateApplicationRequest struct {
	ID          string          `json:"id" binding:"required,max=128"`
	Name        string          `json:"name" binding:"required,max=256"`
	Description string          `json:"description" binding:"max=4096"`
	Icon        string          `json:"icon" binding:"max=256"`
	HomePath    string          `json:"home_path" binding:"max=2048"`
	Status      string          `json:"status" binding:"required,oneof=active disabled"`
	SortOrder   int64           `json:"sort_order" binding:"gte=-1000000000,lte=1000000000"`
	Metadata    json.RawMessage `json:"metadata" swaggertype:"object"`
	Version     int64           `json:"version" binding:"required,gt=0"`
}
type DeleteApplicationRequest struct {
	ID      string `json:"id" binding:"required,max=128"`
	Version int64  `json:"version" binding:"required,gt=0"`
}

// CreateApplication godoc
// @Summary Create an application
// @Tags applications
// @Security Bearer
// @Param request body CreateApplicationRequest true "Application"
// @Success 200 {object} Response{body=application.Record}
// @Router /api/v1/applications/create [post]
func (h *ApplicationHandler) Create(c *gin.Context) {
	var r CreateApplicationRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Create(c.Request.Context(), application.Input{Code: r.Code, Name: r.Name, Description: r.Description, Icon: r.Icon, HomePath: r.HomePath, Status: r.Status, SortOrder: r.SortOrder, Metadata: r.Metadata})
	h.respond(c, v, e)
}

// GetApplication godoc
// @Summary Get an application
// @Tags applications
// @Security Bearer
// @Param request body ApplicationIDRequest true "Application"
// @Success 200 {object} Response{body=application.Record}
// @Router /api/v1/applications/get [post]
func (h *ApplicationHandler) Get(c *gin.Context) {
	var r ApplicationIDRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Get(c.Request.Context(), r.ID)
	h.respond(c, v, e)
}

// PageApplications godoc
// @Summary Page applications
// @Tags applications
// @Security Bearer
// @Param request body ApplicationPageRequest true "Filters"
// @Success 200 {object} Response{body=application.Page}
// @Router /api/v1/applications/page [post]
func (h *ApplicationHandler) Page(c *gin.Context) {
	var r ApplicationPageRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Page(c.Request.Context(), application.PageInput{Request: r.Request, IDs: r.IDs, Codes: r.Codes, Statuses: r.Statuses, CreatedAtFrom: r.CreatedAtFrom, CreatedAtTo: r.CreatedAtTo, Sort: r.Sort})
	h.respond(c, v, e)
}

// UpdateApplication godoc
// @Summary Update an application
// @Tags applications
// @Security Bearer
// @Param request body UpdateApplicationRequest true "Application"
// @Success 200 {object} Response{body=application.Record}
// @Router /api/v1/applications/update [post]
func (h *ApplicationHandler) Update(c *gin.Context) {
	var r UpdateApplicationRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Update(c.Request.Context(), application.UpdateInput{ID: r.ID, Name: r.Name, Description: r.Description, Icon: r.Icon, HomePath: r.HomePath, Status: r.Status, SortOrder: r.SortOrder, Metadata: r.Metadata, Version: r.Version})
	h.respond(c, v, e)
}

// DeleteApplication godoc
// @Summary Delete an empty application
// @Tags applications
// @Security Bearer
// @Param request body DeleteApplicationRequest true "Application"
// @Success 200 {object} Response
// @Router /api/v1/applications/delete [post]
func (h *ApplicationHandler) Delete(c *gin.Context) {
	var r DeleteApplicationRequest
	if !h.bind(c, &r) {
		return
	}
	e := h.service.Delete(c.Request.Context(), r.ID, r.Version)
	h.respond(c, gin.H{}, e)
}
func (h *ApplicationHandler) bind(c *gin.Context, v any) bool {
	if e := c.ShouldBindJSON(v); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid application request", e))
		return false
	}
	return true
}
func (h *ApplicationHandler) respond(c *gin.Context, v any, e error) {
	switch {
	case e == nil:
		OK(c, v)
	case errors.Is(e, application.ErrInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid application", e))
	case errors.Is(e, application.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("application not found"))
	case errors.Is(e, application.ErrConflict):
		Fail(c, h.logger, apperror.Conflict("application conflict", e))
	default:
		Fail(c, h.logger, apperror.Internal(e))
	}
}
