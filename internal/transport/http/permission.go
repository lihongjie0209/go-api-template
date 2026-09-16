package httptransport

import (
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/permission"
)

type PermissionHandler struct {
	service *permission.Service
	logger  *slog.Logger
}
type PermissionIDRequest struct {
	ID string `json:"id" binding:"required,max=128"`
}
type PermissionMutationRequest struct {
	ID            string  `json:"id" binding:"omitempty,max=128"`
	ParentID      *string `json:"parent_id" binding:"omitempty,max=128"`
	PermissionKey string  `json:"permission_key" binding:"required,max=128"`
	Name          string  `json:"name" binding:"required,max=256"`
	NodeType      string  `json:"node_type" binding:"required"`
	Resource      string  `json:"resource" binding:"max=256"`
	Action        string  `json:"action" binding:"max=256"`
	Description   string  `json:"description" binding:"max=4096"`
	SortOrder     int64   `json:"sort_order" binding:"gte=-1000000000,lte=1000000000"`
	Status        string  `json:"status" binding:"required"`
	Version       int64   `json:"version"`
}
type DeletePermissionRequest struct {
	ID      string `json:"id" binding:"required,max=128"`
	Version int64  `json:"version" binding:"required,gt=0"`
}
type PermissionTreeRequest struct {
	Keyword   string   `json:"keyword" binding:"omitempty,max=256"`
	NodeTypes []string `json:"node_types" binding:"max=20,dive,oneof=group permission"`
	Statuses  []string `json:"statuses" binding:"max=20,dive,oneof=active disabled"`
}

func (r PermissionMutationRequest) input() permission.Input {
	return permission.Input{ParentID: r.ParentID, Key: r.PermissionKey, Name: r.Name, NodeType: r.NodeType, Resource: r.Resource, Action: r.Action, Description: r.Description, SortOrder: r.SortOrder, Status: r.Status}
}

// CreatePermission godoc
// @Summary Create a permission tree node
// @Tags permissions
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PermissionMutationRequest true "Permission"
// @Success 200 {object} Response{body=permission.Record}
// @Router /api/v1/permissions/create [post]
func (h *PermissionHandler) Create(c *gin.Context) {
	var r PermissionMutationRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Create(c.Request.Context(), r.input())
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// GetPermission godoc
// @Summary Get a permission by ID
// @Tags permissions
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PermissionIDRequest true "Permission ID"
// @Success 200 {object} Response{body=permission.Record}
// @Router /api/v1/permissions/get [post]
func (h *PermissionHandler) Get(c *gin.Context) {
	var r PermissionIDRequest
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

// UpdatePermission godoc
// @Summary Update a permission using optimistic locking
// @Tags permissions
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PermissionMutationRequest true "Permission and version"
// @Success 200 {object} Response{body=permission.Record}
// @Router /api/v1/permissions/update [post]
func (h *PermissionHandler) Update(c *gin.Context) {
	var r PermissionMutationRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Update(c.Request.Context(), r.ID, r.Version, r.input())
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// DeletePermission godoc
// @Summary Delete a leaf permission using optimistic locking
// @Tags permissions
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DeletePermissionRequest true "Permission and version"
// @Success 200 {object} Response
// @Router /api/v1/permissions/delete [post]
func (h *PermissionHandler) Delete(c *gin.Context) {
	var r DeletePermissionRequest
	if !h.bind(c, &r) {
		return
	}
	if e := h.service.Delete(c.Request.Context(), r.ID, r.Version); e != nil {
		h.fail(c, e)
		return
	}
	OK(c, gin.H{})
}
func (h *PermissionHandler) bind(c *gin.Context, v any) bool {
	if e := c.ShouldBindJSON(v); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", e))
		return false
	}
	return true
}
func (h *PermissionHandler) fail(c *gin.Context, e error) {
	switch {
	case errors.Is(e, permission.ErrInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid permission", e))
	case errors.Is(e, permission.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("permission not found"))
	case errors.Is(e, permission.ErrConflict), errors.Is(e, permission.ErrHasChildren), errors.Is(e, permission.ErrInUse):
		Fail(c, h.logger, apperror.Conflict("permission conflict", e))
	default:
		Fail(c, h.logger, apperror.Internal(e))
	}
}

func NewPermissionHandler(service *permission.Service, logger *slog.Logger) *PermissionHandler {
	return &PermissionHandler{service: service, logger: logger}
}

// Tree godoc
// @Summary Get the filtered ordered permission tree (matching nodes retain ancestors)
// @Tags permissions
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PermissionTreeRequest true "Filters"
// @Success 200 {object} Response{body=[]permission.TreeNode}
// @Router /api/v1/permissions/tree [post]
func (h *PermissionHandler) Tree(c *gin.Context) {
	var request PermissionTreeRequest
	if !h.bind(c, &request) {
		return
	}
	value, err := h.service.Tree(c.Request.Context(), permission.TreeInput{Keyword: request.Keyword, NodeTypes: request.NodeTypes, Statuses: request.Statuses})
	if err != nil {
		Fail(c, h.logger, apperror.Internal(err))
		return
	}
	OK(c, value)
}
