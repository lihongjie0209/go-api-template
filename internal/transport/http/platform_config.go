package httptransport

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/platformconfig"
	"log/slog"
)

type PlatformConfigHandler struct {
	service *platformconfig.Service
	logger  *slog.Logger
}

func NewPlatformConfigHandler(service *platformconfig.Service, logger *slog.Logger) *PlatformConfigHandler {
	return &PlatformConfigHandler{service, logger}
}

type CreatePlatformConfigRequest struct {
	Key         string          `json:"key" binding:"required"`
	Name        string          `json:"name" binding:"required"`
	Category    string          `json:"category"`
	Value       json.RawMessage `json:"value" binding:"required" swaggertype:"object"`
	Description string          `json:"description"`
	IsPublic    bool            `json:"is_public"`
	Status      string          `json:"status" binding:"required"`
}
type PlatformConfigIDRequest struct {
	ID string `json:"id" binding:"required"`
}
type PlatformConfigKeyRequest struct {
	Key string `json:"key" binding:"required"`
}
type PlatformConfigPageRequest struct {
	pagination.Request
	Keyword       string     `json:"keyword"`
	IDs           []string   `json:"ids"`
	Categories    []string   `json:"categories"`
	ValueTypes    []string   `json:"value_types"`
	Statuses      []string   `json:"statuses"`
	IsPublic      *bool      `json:"is_public"`
	CreatedAtFrom *time.Time `json:"created_at_from"`
	CreatedAtTo   *time.Time `json:"created_at_to"`
}
type PublicPlatformConfigsRequest struct {
	Category string `json:"category"`
}
type UpdatePlatformConfigRequest struct {
	ID          string          `json:"id" binding:"required"`
	Name        string          `json:"name" binding:"required"`
	Category    string          `json:"category"`
	Value       json.RawMessage `json:"value" binding:"required" swaggertype:"object"`
	Description string          `json:"description"`
	IsPublic    bool            `json:"is_public"`
	Status      string          `json:"status" binding:"required"`
	Version     int64           `json:"version" binding:"required,gt=0"`
}
type DeletePlatformConfigRequest struct {
	ID      string `json:"id" binding:"required"`
	Version int64  `json:"version" binding:"required,gt=0"`
}

// Create godoc
// @Summary Create a platform configuration
// @Tags platform-configs
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CreatePlatformConfigRequest true "Config"
// @Success 200 {object} Response{body=platformconfig.Record}
// @Router /api/v1/platform-configs/create [post]
func (h *PlatformConfigHandler) Create(c *gin.Context) {
	var r CreatePlatformConfigRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Create(c.Request.Context(), platformconfig.Input{Key: r.Key, Name: r.Name, Category: r.Category, Value: r.Value, Description: r.Description, IsPublic: r.IsPublic, Status: r.Status})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// Get godoc
// @Summary Get a platform configuration
// @Tags platform-configs
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PlatformConfigIDRequest true "Config"
// @Success 200 {object} Response{body=platformconfig.Record}
// @Router /api/v1/platform-configs/get [post]
func (h *PlatformConfigHandler) Get(c *gin.Context) {
	var r PlatformConfigIDRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Get(c.Request.Context(), r.ID)
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// Page godoc
// @Summary Page platform configurations
// @Tags platform-configs
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PlatformConfigPageRequest true "Filters"
// @Success 200 {object} Response{body=platformconfig.Page}
// @Router /api/v1/platform-configs/page [post]
func (h *PlatformConfigHandler) Page(c *gin.Context) {
	var r PlatformConfigPageRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Page(c.Request.Context(), platformconfig.PageInput{Request: r.Request, Keyword: r.Keyword, IDs: r.IDs, Categories: r.Categories, ValueTypes: r.ValueTypes, Statuses: r.Statuses, IsPublic: r.IsPublic, CreatedAtFrom: r.CreatedAtFrom, CreatedAtTo: r.CreatedAtTo})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// Update godoc
// @Summary Update a platform configuration
// @Tags platform-configs
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body UpdatePlatformConfigRequest true "Config"
// @Success 200 {object} Response{body=platformconfig.Record}
// @Router /api/v1/platform-configs/update [post]
func (h *PlatformConfigHandler) Update(c *gin.Context) {
	var r UpdatePlatformConfigRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Update(c.Request.Context(), platformconfig.UpdateInput{ID: r.ID, Name: r.Name, Category: r.Category, Value: r.Value, Description: r.Description, IsPublic: r.IsPublic, Status: r.Status, Version: r.Version})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// Delete godoc
// @Summary Delete a platform configuration
// @Tags platform-configs
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DeletePlatformConfigRequest true "Config"
// @Success 200 {object} Response
// @Router /api/v1/platform-configs/delete [post]
func (h *PlatformConfigHandler) Delete(c *gin.Context) {
	var r DeletePlatformConfigRequest
	if !h.bind(c, &r) {
		return
	}
	if e := h.service.Delete(c.Request.Context(), r.ID, r.Version); e != nil {
		h.fail(c, e)
		return
	}
	OK(c, gin.H{})
}

// GetPublic godoc
// @Summary Get one explicitly public platform configuration
// @Tags public-configs
// @Accept json
// @Produce json
// @Param request body PlatformConfigKeyRequest true "Key"
// @Success 200 {object} Response{body=platformconfig.PublicView}
// @Router /api/v1/public/platform-configs/get [post]
func (h *PlatformConfigHandler) GetPublic(c *gin.Context) {
	var r PlatformConfigKeyRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.GetPublic(c.Request.Context(), r.Key)
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// ListPublic godoc
// @Summary List explicitly public active platform configurations
// @Tags public-configs
// @Accept json
// @Produce json
// @Param request body PublicPlatformConfigsRequest true "Filter"
// @Success 200 {object} Response{body=[]platformconfig.PublicView}
// @Router /api/v1/public/platform-configs/list [post]
func (h *PlatformConfigHandler) ListPublic(c *gin.Context) {
	var r PublicPlatformConfigsRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.ListPublic(c.Request.Context(), r.Category)
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}
func (h *PlatformConfigHandler) bind(c *gin.Context, v any) bool {
	if e := c.ShouldBindJSON(v); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid platform config request", e))
		return false
	}
	return true
}
func (h *PlatformConfigHandler) fail(c *gin.Context, e error) {
	switch {
	case errors.Is(e, platformconfig.ErrInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid platform config", e))
	case errors.Is(e, platformconfig.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("platform config not found"))
	case errors.Is(e, platformconfig.ErrConflict):
		Fail(c, h.logger, apperror.Conflict("platform config conflict", e))
	default:
		Fail(c, h.logger, apperror.Internal(e))
	}
}
