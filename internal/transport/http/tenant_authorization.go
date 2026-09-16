package httptransport

import (
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
)

type TenantAuthorizationHandler struct {
	service *authorization.TenantAuthorizationService
	logger  *slog.Logger
}

func NewTenantAuthorizationHandler(service *authorization.TenantAuthorizationService, logger *slog.Logger) *TenantAuthorizationHandler {
	return &TenantAuthorizationHandler{service: service, logger: logger}
}

type setTenantPermissionsRequest struct {
	TenantID      string   `json:"tenant_id" binding:"required,max=128"`
	Version       int64    `json:"version" binding:"required,gt=0"`
	PermissionIDs []string `json:"permission_ids" binding:"max=1000,dive,required,max=128"`
}
type setAdministratorRequest struct {
	TenantID     string `json:"tenant_id" binding:"required,max=128"`
	MembershipID string `json:"membership_id" binding:"required,max=128"`
	Enabled      bool   `json:"enabled"`
}
type createTenantRoleRequest struct {
	Code          string   `json:"code" binding:"required,max=63"`
	Name          string   `json:"name" binding:"required,max=256"`
	Description   string   `json:"description" binding:"max=4096"`
	PermissionIDs []string `json:"permission_ids" binding:"max=1000,dive,required,max=128"`
}
type setRolePermissionsRequest struct {
	RoleID        string   `json:"role_id" binding:"required,max=128"`
	Version       int64    `json:"version" binding:"required,gt=0"`
	PermissionIDs []string `json:"permission_ids" binding:"max=1000,dive,required,max=128"`
}
type setMemberRolesRequest struct {
	MembershipID string   `json:"membership_id" binding:"required,max=128"`
	Version      int64    `json:"version" binding:"required,gt=0"`
	RoleIDs      []string `json:"role_ids" binding:"max=1000,dive,required,max=128"`
}
type memberRolesRequest struct {
	MembershipID string `json:"membership_id" binding:"required,max=128"`
}
type effectivePermissionsRequest struct {
	MembershipID string `json:"membership_id" binding:"omitempty,max=128"`
}
type TenantRoleIDRequest struct {
	ID string `json:"id" binding:"required,max=128"`
}
type TenantRolePageRequest struct {
	pagination.Request
	Keyword       string     `json:"keyword"`
	IDs           []string   `json:"ids"`
	Statuses      []string   `json:"statuses"`
	CreatedAtFrom *time.Time `json:"created_at_from"`
	CreatedAtTo   *time.Time `json:"created_at_to"`
}
type UpdateTenantRoleRequest struct {
	ID          string `json:"id" binding:"required,max=128"`
	Name        string `json:"name" binding:"required,max=256"`
	Description string `json:"description" binding:"max=4096"`
	Status      string `json:"status" binding:"required"`
	Version     int64  `json:"version" binding:"required,gt=0"`
}
type DeleteTenantRoleRequest struct {
	ID      string `json:"id" binding:"required,max=128"`
	Version int64  `json:"version" binding:"required,gt=0"`
}

// SetTenantPermissions godoc
// @Summary Replace a tenant's permission ceiling
// @Tags tenant-authorization
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body setTenantPermissionsRequest true "Tenant permissions"
// @Success 200 {object} Response
// @Router /api/v1/platform/tenant-authorization/permissions/set [post]
func (h *TenantAuthorizationHandler) SetTenantPermissions(c *gin.Context) {
	var request setTenantPermissionsRequest
	if !h.bind(c, &request) {
		return
	}
	h.respond(c, h.service.SetTenantPermissions(c.Request.Context(), request.TenantID, request.Version, request.PermissionIDs))
}

// SetAdministrator godoc
// @Summary Add or remove a tenant administrator
// @Tags tenant-authorization
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body setAdministratorRequest true "Administrator"
// @Success 200 {object} Response
// @Router /api/v1/tenant-authorization/administrators/set [post]
// @Router /api/v1/platform/tenant-authorization/administrators/set [post]
func (h *TenantAuthorizationHandler) SetAdministrator(c *gin.Context) {
	var request setAdministratorRequest
	if !h.bind(c, &request) {
		return
	}
	h.respond(c, h.service.SetAdministrator(c.Request.Context(), request.TenantID, request.MembershipID, request.Enabled))
}

// CreateRole godoc
// @Summary Create a tenant role from the caller's effective permissions
// @Tags tenant-authorization
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body createTenantRoleRequest true "Role"
// @Success 200 {object} Response{body=authorization.TenantRole}
// @Router /api/v1/tenant-roles/create [post]
func (h *TenantAuthorizationHandler) CreateRole(c *gin.Context) {
	var request createTenantRoleRequest
	if !h.bind(c, &request) {
		return
	}
	role, err := h.service.CreateRole(c.Request.Context(), request.Code, request.Name, request.Description, request.PermissionIDs)
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, role)
}

// GetRole godoc
// @Summary Get a tenant role
// @Tags tenant-authorization
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body TenantRoleIDRequest true "Role"
// @Success 200 {object} Response{body=authorization.TenantRole}
// @Router /api/v1/tenant-roles/get [post]
func (h *TenantAuthorizationHandler) GetRole(c *gin.Context) {
	var r TenantRoleIDRequest
	if !h.bind(c, &r) {
		return
	}
	role, err := h.service.GetRole(c.Request.Context(), r.ID)
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, role)
}

// PageRoles godoc
// @Summary Page tenant roles
// @Tags tenant-authorization
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body TenantRolePageRequest true "Filters"
// @Success 200 {object} Response{body=authorization.TenantRolePage}
// @Router /api/v1/tenant-roles/page [post]
func (h *TenantAuthorizationHandler) PageRoles(c *gin.Context) {
	var r TenantRolePageRequest
	if !h.bind(c, &r) {
		return
	}
	page, err := h.service.PageRoles(c.Request.Context(), authorization.RolePageInput{
		Request:       r.Request,
		Keyword:       r.Keyword,
		IDs:           r.IDs,
		Statuses:      r.Statuses,
		CreatedAtFrom: r.CreatedAtFrom,
		CreatedAtTo:   r.CreatedAtTo,
	})
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, page)
}

// UpdateRole godoc
// @Summary Update a tenant role
// @Tags tenant-authorization
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body UpdateTenantRoleRequest true "Role"
// @Success 200 {object} Response{body=authorization.TenantRole}
// @Router /api/v1/tenant-roles/update [post]
func (h *TenantAuthorizationHandler) UpdateRole(c *gin.Context) {
	var r UpdateTenantRoleRequest
	if !h.bind(c, &r) {
		return
	}
	role, err := h.service.UpdateRole(c.Request.Context(), r.ID, r.Name, r.Description, r.Status, r.Version)
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, role)
}

// DeleteRole godoc
// @Summary Delete a tenant role and its assignments
// @Tags tenant-authorization
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DeleteTenantRoleRequest true "Role"
// @Success 200 {object} Response
// @Router /api/v1/tenant-roles/delete [post]
func (h *TenantAuthorizationHandler) DeleteRole(c *gin.Context) {
	var r DeleteTenantRoleRequest
	if !h.bind(c, &r) {
		return
	}
	h.respond(c, h.service.DeleteRole(c.Request.Context(), r.ID, r.Version))
}

// RolePermissions godoc
// @Summary List displayable permissions assigned to a tenant role
// @Tags tenant-authorization
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body TenantRoleIDRequest true "Role"
// @Success 200 {object} Response{body=[]authorization.PermissionView}
// @Router /api/v1/tenant-roles/permissions/get [post]
func (h *TenantAuthorizationHandler) RolePermissions(c *gin.Context) {
	var r TenantRoleIDRequest
	if !h.bind(c, &r) {
		return
	}
	permissions, err := h.service.RolePermissions(c.Request.Context(), r.ID)
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, permissions)
}

// MemberRoles godoc
// @Summary List displayable roles assigned to a tenant member
// @Tags tenant-authorization
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body memberRolesRequest true "Member"
// @Success 200 {object} Response{body=[]authorization.MemberRoleView}
// @Router /api/v1/tenant-members/roles/get [post]
func (h *TenantAuthorizationHandler) MemberRoles(c *gin.Context) {
	var request memberRolesRequest
	if !h.bind(c, &request) {
		return
	}
	roles, err := h.service.MemberRoles(c.Request.Context(), request.MembershipID)
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, roles)
}

// SetRolePermissions godoc
// @Summary Replace role permissions with optimistic locking
// @Tags tenant-authorization
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body setRolePermissionsRequest true "Role permissions"
// @Success 200 {object} Response
// @Router /api/v1/tenant-roles/permissions/set [post]
func (h *TenantAuthorizationHandler) SetRolePermissions(c *gin.Context) {
	var request setRolePermissionsRequest
	if !h.bind(c, &request) {
		return
	}
	h.respond(c, h.service.SetRolePermissions(c.Request.Context(), request.RoleID, request.Version, request.PermissionIDs))
}

// SetMemberRoles godoc
// @Summary Replace a tenant member's roles
// @Tags tenant-authorization
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body setMemberRolesRequest true "Member roles"
// @Success 200 {object} Response
// @Router /api/v1/tenant-members/roles/set [post]
func (h *TenantAuthorizationHandler) SetMemberRoles(c *gin.Context) {
	var request setMemberRolesRequest
	if !h.bind(c, &request) {
		return
	}
	h.respond(c, h.service.SetMemberRoles(c.Request.Context(), request.MembershipID, request.Version, request.RoleIDs))
}

// EffectivePermissions godoc
// @Summary List effective permission IDs for a tenant member
// @Tags tenant-authorization
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body effectivePermissionsRequest true "Member"
// @Success 200 {object} Response{body=[]string}
// @Router /api/v1/tenant-authorization/effective-permissions [post]
func (h *TenantAuthorizationHandler) EffectivePermissions(c *gin.Context) {
	var request effectivePermissionsRequest
	if !h.bind(c, &request) {
		return
	}
	permissions, err := h.service.EffectivePermissions(c.Request.Context(), request.MembershipID)
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, permissions)
}

func (h *TenantAuthorizationHandler) bind(c *gin.Context, request any) bool {
	if err := c.ShouldBindJSON(request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", err))
		return false
	}
	return true
}
func (h *TenantAuthorizationHandler) respond(c *gin.Context, err error) {
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, gin.H{})
}
func (h *TenantAuthorizationHandler) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, authorization.ErrTenantAuthorizationInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid tenant authorization request", err))
	case errors.Is(err, authorization.ErrTenantAuthorizationForbidden):
		Fail(c, h.logger, apperror.Forbidden("tenant authorization denied"))
	case errors.Is(err, authorization.ErrTenantAuthorizationNotFound):
		Fail(c, h.logger, apperror.NotFound("tenant authorization resource not found"))
	case errors.Is(err, authorization.ErrTenantAuthorizationConflict):
		Fail(c, h.logger, apperror.Conflict("tenant authorization conflict", err))
	default:
		Fail(c, h.logger, apperror.Internal(err))
	}
}
