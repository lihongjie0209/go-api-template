package httptransport

import (
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	"github.com/lihongjie0209/go-api-template/internal/resourcecatalog"
)

type ResourceCatalogHandler struct {
	service *resourcecatalog.Service
	logger  *slog.Logger
}

func NewResourceCatalogHandler(service *resourcecatalog.Service, logger *slog.Logger) *ResourceCatalogHandler {
	return &ResourceCatalogHandler{service: service, logger: logger}
}

type CatalogIDRequest struct {
	ID string `json:"id" binding:"required,uuid"`
}
type CatalogDeleteRequest struct {
	ID      string `json:"id" binding:"required,uuid"`
	Version int64  `json:"version" binding:"required,gt=0"`
}
type CatalogPageRequest struct {
	Page                int                          `json:"page"`
	PageSize            int                          `json:"page_size"`
	Keyword             string                       `json:"keyword" binding:"max=256"`
	IDs                 []string                     `json:"ids" binding:"max=200,dive,uuid"`
	Statuses            []string                     `json:"statuses" binding:"max=2,dive,oneof=active disabled"`
	Sources             []string                     `json:"sources" binding:"max=2,dive,oneof=code manual"`
	ResourceID          string                       `json:"resource_id" binding:"omitempty,uuid"`
	ResourceKey         string                       `json:"resource_key" binding:"omitempty,max=128"`
	Scope               resourceaction.ResourceScope `json:"scope" binding:"omitempty,oneof=platform tenant principal"`
	ActiveResourcesOnly bool                         `json:"active_resources_only"`
}

func (r CatalogPageRequest) input() resourcecatalog.PageInput {
	return resourcecatalog.PageInput{Request: pagination.Request{Page: r.Page, PageSize: r.PageSize, Keyword: r.Keyword}, IDs: r.IDs, Statuses: r.Statuses, Sources: r.Sources, ResourceID: r.ResourceID, ResourceKey: r.ResourceKey, Scope: r.Scope, ActiveResourcesOnly: r.ActiveResourcesOnly}
}

type CatalogResourceRequest struct {
	ID          string                       `json:"id" binding:"omitempty,uuid"`
	Key         string                       `json:"resource_key" binding:"required,max=128"`
	Name        string                       `json:"name" binding:"required,max=256"`
	Description string                       `json:"description" binding:"max=4096"`
	Scope       resourceaction.ResourceScope `json:"scope" binding:"required,oneof=platform tenant principal"`
	Status      string                       `json:"status" binding:"required,oneof=active disabled"`
	Version     int64                        `json:"version"`
}

func (r CatalogResourceRequest) input() resourcecatalog.ResourceInput {
	return resourcecatalog.ResourceInput{Key: r.Key, Name: r.Name, Description: r.Description, Scope: r.Scope, Status: r.Status}
}

type CatalogActionRequest struct {
	ID          string `json:"id" binding:"omitempty,uuid"`
	ResourceID  string `json:"resource_id" binding:"required,uuid"`
	Key         string `json:"action_key" binding:"required,max=64"`
	Name        string `json:"name" binding:"required,max=256"`
	Description string `json:"description" binding:"max=4096"`
	Status      string `json:"status" binding:"required,oneof=active disabled"`
	Version     int64  `json:"version"`
}

func (r CatalogActionRequest) input() resourcecatalog.ActionInput {
	return resourcecatalog.ActionInput{ResourceID: r.ResourceID, Key: r.Key, Name: r.Name, Description: r.Description, Status: r.Status}
}

func (h *ResourceCatalogHandler) bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", err))
		return false
	}
	return true
}
func (h *ResourceCatalogHandler) respond(c *gin.Context, result any, err error) {
	if err == nil {
		OK(c, result)
		return
	}
	switch {
	case errors.Is(err, resourcecatalog.ErrInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid catalog entry", err))
	case errors.Is(err, resourcecatalog.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("catalog entry not found"))
	case errors.Is(err, resourcecatalog.ErrConflict), errors.Is(err, resourcecatalog.ErrCodeOwned), errors.Is(err, resourcecatalog.ErrInUse):
		Fail(c, h.logger, apperror.Conflict("catalog entry conflict", err))
	case errors.Is(err, resourcecatalog.ErrStaleVersion):
		Fail(c, h.logger, apperror.StaleVersion(err))
	default:
		Fail(c, h.logger, apperror.Internal(err))
	}
}

// CreateCatalogResource godoc
// @Summary Create a manually owned authorization resource
// @Tags authorization-catalog
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CatalogResourceRequest true "Resource"
// @Success 200 {object} Response{body=resourcecatalog.Resource}
// @Router /api/v1/authorization/resources/create [post]
func (h *ResourceCatalogHandler) CreateResource(c *gin.Context) {
	var r CatalogResourceRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.CreateResource(c.Request.Context(), r.input())
	h.respond(c, v, e)
}

// GetCatalogResource godoc
// @Summary Get an authorization resource
// @Tags authorization-catalog
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CatalogIDRequest true "Resource ID"
// @Success 200 {object} Response{body=resourcecatalog.Resource}
// @Router /api/v1/authorization/resources/get [post]
func (h *ResourceCatalogHandler) GetResource(c *gin.Context) {
	var r CatalogIDRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.GetResource(c.Request.Context(), r.ID)
	h.respond(c, v, e)
}

// GetCatalogResourceUpdateTarget godoc
// @Summary Get a manual authorization resource editing target
// @Tags authorization-catalog
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CatalogIDRequest true "Resource ID"
// @Success 200 {object} Response{body=resourcecatalog.ResourceUpdateTarget}
// @Router /api/v1/authorization/resources/update-target [post]
func (h *ResourceCatalogHandler) ResourceUpdateTarget(c *gin.Context) {
	var r CatalogIDRequest
	if !h.bind(c, &r) {
		return
	}
	v, err := h.service.ResourceUpdateTarget(c.Request.Context(), r.ID)
	h.respond(c, v, err)
}

// GetCatalogResourceDeleteTarget godoc
// @Summary Get a manual authorization resource deletion target
// @Tags authorization-catalog
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CatalogIDRequest true "Resource ID"
// @Success 200 {object} Response{body=resourcecatalog.ResourceDeleteTarget}
// @Router /api/v1/authorization/resources/delete-target [post]
func (h *ResourceCatalogHandler) ResourceDeleteTarget(c *gin.Context) {
	var r CatalogIDRequest
	if !h.bind(c, &r) {
		return
	}
	v, err := h.service.ResourceDeleteTarget(c.Request.Context(), r.ID)
	h.respond(c, v, err)
}

// PageCatalogResources godoc
// @Summary Page authorization resources
// @Tags authorization-catalog
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CatalogPageRequest true "Page request"
// @Success 200 {object} Response{body=resourcecatalog.ResourcePage}
// @Router /api/v1/authorization/resources/page [post]
func (h *ResourceCatalogHandler) PageResources(c *gin.Context) {
	var r CatalogPageRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.PageResources(c.Request.Context(), r.input())
	h.respond(c, v, e)
}

// UpdateCatalogResource godoc
// @Summary Update a manually owned authorization resource
// @Tags authorization-catalog
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CatalogResourceRequest true "Resource and version"
// @Success 200 {object} Response{body=resourcecatalog.Resource}
// @Router /api/v1/authorization/resources/update [post]
func (h *ResourceCatalogHandler) UpdateResource(c *gin.Context) {
	var r CatalogResourceRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.UpdateResource(c.Request.Context(), r.ID, r.Version, r.input())
	h.respond(c, v, e)
}

// DeleteCatalogResource godoc
// @Summary Soft-delete a manually owned authorization resource
// @Tags authorization-catalog
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CatalogDeleteRequest true "Resource and version"
// @Success 200 {object} Response
// @Router /api/v1/authorization/resources/delete [post]
func (h *ResourceCatalogHandler) DeleteResource(c *gin.Context) {
	var r CatalogDeleteRequest
	if !h.bind(c, &r) {
		return
	}
	h.respond(c, gin.H{}, h.service.DeleteResource(c.Request.Context(), r.ID, r.Version))
}

// CreateCatalogAction godoc
// @Summary Create a manually owned resource action
// @Tags authorization-catalog
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CatalogActionRequest true "Action"
// @Success 200 {object} Response{body=resourcecatalog.Action}
// @Router /api/v1/authorization/actions/create [post]
func (h *ResourceCatalogHandler) CreateAction(c *gin.Context) {
	var r CatalogActionRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.CreateAction(c.Request.Context(), r.input())
	h.respond(c, v, e)
}

// GetCatalogAction godoc
// @Summary Get a resource action
// @Tags authorization-catalog
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CatalogIDRequest true "Action ID"
// @Success 200 {object} Response{body=resourcecatalog.Action}
// @Router /api/v1/authorization/actions/get [post]
func (h *ResourceCatalogHandler) GetAction(c *gin.Context) {
	var r CatalogIDRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.GetAction(c.Request.Context(), r.ID)
	h.respond(c, v, e)
}

// GetCatalogActionUpdateTarget godoc
// @Summary Get a manual authorization action editing target
// @Tags authorization-catalog
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CatalogIDRequest true "Action ID"
// @Success 200 {object} Response{body=resourcecatalog.ActionUpdateTarget}
// @Router /api/v1/authorization/actions/update-target [post]
func (h *ResourceCatalogHandler) ActionUpdateTarget(c *gin.Context) {
	var r CatalogIDRequest
	if !h.bind(c, &r) {
		return
	}
	v, err := h.service.ActionUpdateTarget(c.Request.Context(), r.ID)
	h.respond(c, v, err)
}

// GetCatalogActionDeleteTarget godoc
// @Summary Get a manual authorization action deletion target
// @Tags authorization-catalog
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CatalogIDRequest true "Action ID"
// @Success 200 {object} Response{body=resourcecatalog.ActionDeleteTarget}
// @Router /api/v1/authorization/actions/delete-target [post]
func (h *ResourceCatalogHandler) ActionDeleteTarget(c *gin.Context) {
	var r CatalogIDRequest
	if !h.bind(c, &r) {
		return
	}
	v, err := h.service.ActionDeleteTarget(c.Request.Context(), r.ID)
	h.respond(c, v, err)
}

// PageCatalogActions godoc
// @Summary Page resource actions
// @Tags authorization-catalog
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CatalogPageRequest true "Page request"
// @Success 200 {object} Response{body=resourcecatalog.ActionPage}
// @Router /api/v1/authorization/actions/page [post]
func (h *ResourceCatalogHandler) PageActions(c *gin.Context) {
	var r CatalogPageRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.PageActions(c.Request.Context(), r.input())
	h.respond(c, v, e)
}

// UpdateCatalogAction godoc
// @Summary Update a manually owned resource action
// @Tags authorization-catalog
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CatalogActionRequest true "Action and version"
// @Success 200 {object} Response{body=resourcecatalog.Action}
// @Router /api/v1/authorization/actions/update [post]
func (h *ResourceCatalogHandler) UpdateAction(c *gin.Context) {
	var r CatalogActionRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.UpdateAction(c.Request.Context(), r.ID, r.Version, r.input())
	h.respond(c, v, e)
}

// DeleteCatalogAction godoc
// @Summary Soft-delete a manually owned resource action
// @Tags authorization-catalog
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CatalogDeleteRequest true "Action and version"
// @Success 200 {object} Response
// @Router /api/v1/authorization/actions/delete [post]
func (h *ResourceCatalogHandler) DeleteAction(c *gin.Context) {
	var r CatalogDeleteRequest
	if !h.bind(c, &r) {
		return
	}
	h.respond(c, gin.H{}, h.service.DeleteAction(c.Request.Context(), r.ID, r.Version))
}
