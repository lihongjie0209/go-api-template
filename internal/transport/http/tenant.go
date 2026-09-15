package httptransport

import (
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/tenant"
)

type TenantHandler struct {
	service *tenant.Service
	logger  *slog.Logger
}

func NewTenantHandler(service *tenant.Service, logger *slog.Logger) *TenantHandler {
	return &TenantHandler{service: service, logger: logger}
}

type CreateTenantRequest struct {
	Code          string `json:"code" binding:"required,max=63"`
	Name          string `json:"name" binding:"required,max=512"`
	Description   string `json:"description"`
	OwnerUsername string `json:"owner_username" binding:"required,max=64"`
}
type GetTenantRequest struct {
	ID string `json:"id" binding:"required"`
}
type PageTenantRequest struct {
	Page          int             `json:"page"`
	PageSize      int             `json:"page_size"`
	Keyword       string          `json:"keyword" binding:"omitempty,max=256"`
	IDs           []string        `json:"ids" binding:"max=200"`
	Statuses      []tenant.Status `json:"statuses" binding:"max=20,dive,oneof=active disabled"`
	CreatedAtFrom *time.Time      `json:"created_at_from"`
	CreatedAtTo   *time.Time      `json:"created_at_to"`
}
type PageTenantResponse struct {
	Items    []tenant.View `json:"items"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	Total    int64         `json:"total"`
}
type UpdateTenantRequest struct {
	ID          string        `json:"id" binding:"required"`
	Name        string        `json:"name" binding:"required,max=512"`
	Description string        `json:"description"`
	Status      tenant.Status `json:"status" binding:"required"`
	Version     int64         `json:"version" binding:"required,gt=0"`
}
type DeleteTenantRequest struct {
	ID      string `json:"id" binding:"required"`
	Version int64  `json:"version" binding:"required,gt=0"`
}

// CreateTenant godoc
// @Summary Create a tenant and its owner membership
// @Tags tenants
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CreateTenantRequest true "Tenant"
// @Success 200 {object} Response{body=tenant.View}
// @Router /api/v1/tenants/create [post]
func (h *TenantHandler) Create(c *gin.Context) {
	var request CreateTenantRequest
	if !h.bind(c, &request) {
		return
	}
	view, err := h.service.Create(c.Request.Context(), tenant.CreateInput{Code: request.Code, Name: request.Name, Description: request.Description, OwnerUsername: request.OwnerUsername})
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, view)
}

// GetTenant godoc
// @Summary Get a tenant by ID
// @Tags tenants
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body GetTenantRequest true "Tenant ID"
// @Success 200 {object} Response{body=tenant.View}
// @Router /api/v1/tenants/get [post]
func (h *TenantHandler) Get(c *gin.Context) {
	var request GetTenantRequest
	if !h.bind(c, &request) {
		return
	}
	view, err := h.service.Get(c.Request.Context(), request.ID)
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, view)
}

// PageTenants godoc
// @Summary Page tenants visible to the caller
// @Tags tenants
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PageTenantRequest true "Pagination"
// @Success 200 {object} Response{body=PageTenantResponse}
// @Router /api/v1/tenants/page [post]
func (h *TenantHandler) Page(c *gin.Context) {
	var request PageTenantRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.Page(c.Request.Context(), tenant.PageInput{
		Request: pagination.Request{Page: request.Page, PageSize: request.PageSize, Keyword: request.Keyword},
		IDs:     request.IDs, Statuses: request.Statuses, CreatedAtFrom: request.CreatedAtFrom, CreatedAtTo: request.CreatedAtTo,
	})
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, result)
}

// UpdateTenant godoc
// @Summary Update a tenant using optimistic locking
// @Tags tenants
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body UpdateTenantRequest true "Tenant and version"
// @Success 200 {object} Response{body=tenant.View}
// @Router /api/v1/tenants/update [post]
func (h *TenantHandler) Update(c *gin.Context) {
	var request UpdateTenantRequest
	if !h.bind(c, &request) {
		return
	}
	view, err := h.service.Update(c.Request.Context(), tenant.UpdateInput{ID: request.ID, Name: request.Name, Description: request.Description, Status: request.Status, Version: request.Version})
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, view)
}

// DeleteTenant godoc
// @Summary Logically delete a tenant using optimistic locking
// @Tags tenants
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DeleteTenantRequest true "Tenant and version"
// @Success 200 {object} Response
// @Router /api/v1/tenants/delete [post]
func (h *TenantHandler) Delete(c *gin.Context) {
	var request DeleteTenantRequest
	if !h.bind(c, &request) {
		return
	}
	if err := h.service.Delete(c.Request.Context(), request.ID, request.Version); err != nil {
		h.fail(c, err)
		return
	}
	OK(c, gin.H{})
}

func (h *TenantHandler) bind(c *gin.Context, value any) bool {
	if err := c.ShouldBindJSON(value); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", err))
		return false
	}
	return true
}

func (h *TenantHandler) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, tenant.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("tenant not found"))
	case errors.Is(err, tenant.ErrForbidden):
		Fail(c, h.logger, apperror.Forbidden("tenant access denied"))
	case errors.Is(err, tenant.ErrConflict):
		Fail(c, h.logger, apperror.Conflict("tenant version or unique constraint conflict", err))
	case errors.Is(err, tenant.ErrInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid tenant input", err))
	default:
		Fail(c, h.logger, apperror.Internal(err))
	}
}
