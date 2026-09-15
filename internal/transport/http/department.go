package httptransport

import (
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/tenant"
)

type DepartmentHandler struct {
	service *tenant.DepartmentService
	logger  *slog.Logger
}

func NewDepartmentHandler(service *tenant.DepartmentService, logger *slog.Logger) *DepartmentHandler {
	return &DepartmentHandler{service, logger}
}

type CreateDepartmentRequest struct {
	ParentID  *string `json:"parent_id"`
	Code      string  `json:"code" binding:"required,max=63"`
	Name      string  `json:"name" binding:"required,max=512"`
	SortOrder int64   `json:"sort_order"`
}
type DepartmentIDRequest struct {
	ID string `json:"id" binding:"required"`
}
type DepartmentTreeRequest struct {
	Keyword string `json:"keyword" binding:"omitempty,max=256"`
}
type UpdateDepartmentRequest struct {
	ID        string  `json:"id" binding:"required"`
	ParentID  *string `json:"parent_id"`
	Name      string  `json:"name" binding:"required,max=512"`
	SortOrder int64   `json:"sort_order"`
	Version   int64   `json:"version" binding:"required,gt=0"`
}
type DeleteDepartmentRequest struct {
	ID      string `json:"id" binding:"required"`
	Version int64  `json:"version" binding:"required,gt=0"`
}
type SetDepartmentMembersRequest struct {
	DepartmentID string                              `json:"department_id" binding:"required"`
	Members      []tenant.DepartmentMemberAssignment `json:"members" binding:"max=1000,dive"`
}

// Create godoc
// @Summary Create a tenant department
// @Tags tenant-departments
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CreateDepartmentRequest true "Department"
// @Success 200 {object} Response{body=tenant.Department}
// @Router /api/v1/tenant-departments/create [post]
func (h *DepartmentHandler) Create(c *gin.Context) {
	var r CreateDepartmentRequest
	if !h.bind(c, &r) {
		return
	}
	value, err := h.service.Create(c.Request.Context(), tenant.DepartmentInput{ParentID: r.ParentID, Code: r.Code, Name: r.Name, SortOrder: r.SortOrder})
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, value)
}

// Get godoc
// @Summary Get a tenant department
// @Tags tenant-departments
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DepartmentIDRequest true "Department"
// @Success 200 {object} Response{body=tenant.Department}
// @Router /api/v1/tenant-departments/get [post]
func (h *DepartmentHandler) Get(c *gin.Context) {
	var r DepartmentIDRequest
	if !h.bind(c, &r) {
		return
	}
	value, err := h.service.Get(c.Request.Context(), r.ID)
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, value)
}

// Tree godoc
// @Summary Return the tenant department tree
// @Tags tenant-departments
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DepartmentTreeRequest true "Search"
// @Success 200 {object} Response{body=[]tenant.DepartmentNode}
// @Router /api/v1/tenant-departments/tree [post]
func (h *DepartmentHandler) Tree(c *gin.Context) {
	var r DepartmentTreeRequest
	if !h.bind(c, &r) {
		return
	}
	value, err := h.service.Tree(c.Request.Context(), r.Keyword)
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, value)
}

// Update godoc
// @Summary Update or move a tenant department
// @Tags tenant-departments
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body UpdateDepartmentRequest true "Department"
// @Success 200 {object} Response{body=tenant.Department}
// @Router /api/v1/tenant-departments/update [post]
func (h *DepartmentHandler) Update(c *gin.Context) {
	var r UpdateDepartmentRequest
	if !h.bind(c, &r) {
		return
	}
	value, err := h.service.Update(c.Request.Context(), tenant.DepartmentUpdate{ID: r.ID, ParentID: r.ParentID, Name: r.Name, SortOrder: r.SortOrder, Version: r.Version})
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, value)
}

// Delete godoc
// @Summary Delete an empty tenant department
// @Tags tenant-departments
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DeleteDepartmentRequest true "Department"
// @Success 200 {object} Response
// @Router /api/v1/tenant-departments/delete [post]
func (h *DepartmentHandler) Delete(c *gin.Context) {
	var r DeleteDepartmentRequest
	if !h.bind(c, &r) {
		return
	}
	if err := h.service.Delete(c.Request.Context(), r.ID, r.Version); err != nil {
		h.fail(c, err)
		return
	}
	OK(c, gin.H{})
}

// SetMembers godoc
// @Summary Replace department members and primary-department flags
// @Tags tenant-departments
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body SetDepartmentMembersRequest true "Assignments"
// @Success 200 {object} Response
// @Router /api/v1/tenant-departments/members/set [post]
func (h *DepartmentHandler) SetMembers(c *gin.Context) {
	var r SetDepartmentMembersRequest
	if !h.bind(c, &r) {
		return
	}
	if err := h.service.SetMembers(c.Request.Context(), r.DepartmentID, r.Members); err != nil {
		h.fail(c, err)
		return
	}
	OK(c, gin.H{})
}
func (h *DepartmentHandler) bind(c *gin.Context, v any) bool {
	if err := c.ShouldBindJSON(v); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid department request", err))
		return false
	}
	return true
}
func (h *DepartmentHandler) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, tenant.ErrInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid department", err))
	case errors.Is(err, tenant.ErrForbidden):
		Fail(c, h.logger, apperror.Forbidden("department access denied"))
	case errors.Is(err, tenant.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("department not found"))
	case errors.Is(err, tenant.ErrConflict):
		Fail(c, h.logger, apperror.Conflict("department conflict", err))
	default:
		Fail(c, h.logger, apperror.Internal(err))
	}
}
