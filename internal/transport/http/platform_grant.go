package httptransport

import (
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
)

type PlatformGrantHandler struct {
	service *authorization.PlatformGrantService
	logger  *slog.Logger
}

func NewPlatformGrantHandler(service *authorization.PlatformGrantService, logger *slog.Logger) *PlatformGrantHandler {
	return &PlatformGrantHandler{service: service, logger: logger}
}

type PlatformGrantMutationRequest struct {
	ID            string `json:"id" binding:"omitempty,uuid"`
	Version       int64  `json:"version"`
	PrincipalType string `json:"principal_type" binding:"required,oneof=user service_account"`
	PrincipalID   string `json:"principal_id" binding:"required,uuid"`
	PermissionID  string `json:"permission_id" binding:"required,uuid"`
}

// UpdatePlatformGrant godoc
// @Summary Update a platform principal permission grant
// @Tags platform-grants
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PlatformGrantMutationRequest true "Grant and version"
// @Success 200 {object} Response{body=authorization.PlatformGrant}
// @Failure 409 {object} Response "Code 30011: stale version; Code 30009: duplicate grant"
// @Router /api/v1/platform/permission-grants/update [post]
func (h *PlatformGrantHandler) Update(c *gin.Context) {
	var r PlatformGrantMutationRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Update(c.Request.Context(), r.ID, r.Version, authorization.PlatformGrantInput{PrincipalType: r.PrincipalType, PrincipalID: r.PrincipalID, PermissionID: r.PermissionID})
	h.respond(c, v, e)
}

type PlatformGrantIDRequest struct {
	ID string `json:"id" binding:"required,uuid"`
}
type PlatformGrantDeleteRequest struct {
	ID      string `json:"id" binding:"required,uuid"`
	Version int64  `json:"version" binding:"required,gt=0"`
}
type PlatformGrantPageRequest struct {
	Page          int      `json:"page"`
	PageSize      int      `json:"page_size"`
	Keyword       string   `json:"keyword" binding:"max=256"`
	PrincipalType string   `json:"principal_type" binding:"omitempty,oneof=user service_account"`
	PrincipalID   string   `json:"principal_id" binding:"omitempty,uuid"`
	PermissionIDs []string `json:"permission_ids" binding:"max=200,dive,uuid"`
}

type PlatformGrantPermissionPageRequest struct {
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
	Keyword  string   `json:"keyword" binding:"max=256"`
	IDs      []string `json:"ids" binding:"max=200,dive,uuid"`
}

// PageGrantablePermissions godoc
// @Summary Page active permissions linked to a platform-scoped action
// @Tags platform-grants
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PlatformGrantPermissionPageRequest true "Page request"
// @Success 200 {object} Response{body=authorization.PlatformGrantablePermissionPage}
// @Failure 400 {object} Response "Code 10001: invalid request"
// @Router /api/v1/platform/permission-grants/permissions/page [post]
func (h *PlatformGrantHandler) PageGrantablePermissions(c *gin.Context) {
	var r PlatformGrantPermissionPageRequest
	if !h.bind(c, &r) {
		return
	}
	v, err := h.service.PageGrantablePermissions(c.Request.Context(), authorization.PlatformGrantablePermissionPageInput{
		Request: pagination.Request{Page: r.Page, PageSize: r.PageSize, Keyword: r.Keyword},
		IDs:     r.IDs,
	})
	h.respond(c, v, err)
}

func (h *PlatformGrantHandler) bind(c *gin.Context, v any) bool {
	if err := c.ShouldBindJSON(v); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", err))
		return false
	}
	return true
}
func (h *PlatformGrantHandler) respond(c *gin.Context, v any, err error) {
	if err == nil {
		OK(c, v)
		return
	}
	switch {
	case errors.Is(err, authorization.ErrPlatformGrantInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid platform grant", err))
	case errors.Is(err, authorization.ErrPlatformGrantNotFound):
		Fail(c, h.logger, apperror.NotFound("platform grant not found"))
	case errors.Is(err, authorization.ErrPlatformGrantConflict):
		Fail(c, h.logger, apperror.Conflict("platform grant conflict", err))
	case errors.Is(err, authorization.ErrPlatformGrantStaleVersion):
		Fail(c, h.logger, apperror.StaleVersion(err))
	default:
		Fail(c, h.logger, apperror.Internal(err))
	}
}

// CreatePlatformGrant godoc
// @Summary Grant a platform permission to a principal
// @Tags platform-grants
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PlatformGrantMutationRequest true "Grant"
// @Success 200 {object} Response{body=authorization.PlatformGrant}
// @Router /api/v1/platform/permission-grants/create [post]
func (h *PlatformGrantHandler) Create(c *gin.Context) {
	var r PlatformGrantMutationRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Create(c.Request.Context(), authorization.PlatformGrantInput{PrincipalType: r.PrincipalType, PrincipalID: r.PrincipalID, PermissionID: r.PermissionID})
	h.respond(c, v, e)
}

// GetPlatformGrant godoc
// @Summary Get a platform principal permission grant
// @Tags platform-grants
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PlatformGrantIDRequest true "Grant ID"
// @Success 200 {object} Response{body=authorization.PlatformGrant}
// @Router /api/v1/platform/permission-grants/get [post]
func (h *PlatformGrantHandler) Get(c *gin.Context) {
	var r PlatformGrantIDRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Get(c.Request.Context(), r.ID)
	h.respond(c, v, e)
}

// GetPlatformGrantUpdateTarget godoc
// @Summary Get the current platform grant edit target
// @Tags platform-grants
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PlatformGrantIDRequest true "Grant ID"
// @Success 200 {object} Response{body=authorization.PlatformGrantUpdateTarget}
// @Failure 400 {object} Response "Code 10001: invalid request"
// @Failure 404 {object} Response "Grant not found"
// @Router /api/v1/platform/permission-grants/update-target [post]
func (h *PlatformGrantHandler) UpdateTarget(c *gin.Context) {
	var r PlatformGrantIDRequest
	if !h.bind(c, &r) {
		return
	}
	v, err := h.service.UpdateTarget(c.Request.Context(), r.ID)
	h.respond(c, v, err)
}

// GetPlatformGrantDeleteTarget godoc
// @Summary Get the current platform grant revocation target
// @Tags platform-grants
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PlatformGrantIDRequest true "Grant ID"
// @Success 200 {object} Response{body=authorization.PlatformGrantDeleteTarget}
// @Failure 400 {object} Response "Code 10001: invalid request"
// @Failure 404 {object} Response "Grant not found"
// @Router /api/v1/platform/permission-grants/delete-target [post]
func (h *PlatformGrantHandler) DeleteTarget(c *gin.Context) {
	var r PlatformGrantIDRequest
	if !h.bind(c, &r) {
		return
	}
	v, err := h.service.DeleteTarget(c.Request.Context(), r.ID)
	h.respond(c, v, err)
}

// PagePlatformGrants godoc
// @Summary Page platform principal permission grants
// @Tags platform-grants
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PlatformGrantPageRequest true "Page request"
// @Success 200 {object} Response{body=authorization.PlatformGrantPage}
// @Router /api/v1/platform/permission-grants/page [post]
func (h *PlatformGrantHandler) Page(c *gin.Context) {
	var r PlatformGrantPageRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Page(c.Request.Context(), authorization.PlatformGrantPageInput{Request: pagination.Request{Page: r.Page, PageSize: r.PageSize, Keyword: r.Keyword}, PrincipalType: r.PrincipalType, PrincipalID: r.PrincipalID, PermissionIDs: r.PermissionIDs})
	h.respond(c, v, e)
}

// DeletePlatformGrant godoc
// @Summary Revoke a platform principal permission grant
// @Tags platform-grants
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PlatformGrantDeleteRequest true "Grant and version"
// @Success 200 {object} Response
// @Failure 409 {object} Response "Code 30011: stale version"
// @Router /api/v1/platform/permission-grants/delete [post]
func (h *PlatformGrantHandler) Delete(c *gin.Context) {
	var r PlatformGrantDeleteRequest
	if !h.bind(c, &r) {
		return
	}
	h.respond(c, gin.H{}, h.service.Delete(c.Request.Context(), r.ID, r.Version))
}
