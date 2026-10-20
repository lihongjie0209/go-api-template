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

type TenantMemberHandler struct {
	service  *tenant.MembershipService
	contexts *tenant.ContextService
	logger   *slog.Logger
}

func NewTenantMemberHandler(service *tenant.MembershipService, contexts *tenant.ContextService, logger *slog.Logger) *TenantMemberHandler {
	return &TenantMemberHandler{service: service, contexts: contexts, logger: logger}
}

type AddTenantMemberRequest struct {
	Username string `json:"username" binding:"required,max=256"`
}
type TenantMemberIDRequest struct {
	ID string `json:"id" binding:"required"`
}
type TenantMemberPageRequest struct {
	Page       int             `json:"page"`
	PageSize   int             `json:"page_size"`
	Keyword    string          `json:"keyword" binding:"omitempty,max=256"`
	IDs        []string        `json:"ids" binding:"max=200"`
	UserIDs    []string        `json:"user_ids" binding:"max=200"`
	Usernames  []string        `json:"usernames" binding:"max=200"`
	Emails     []string        `json:"emails" binding:"max=200"`
	Phones     []string        `json:"phones" binding:"max=200"`
	Statuses   []tenant.Status `json:"statuses" binding:"max=20,dive,oneof=active disabled"`
	JoinedFrom *time.Time      `json:"joined_from"`
	JoinedTo   *time.Time      `json:"joined_to"`
}

// TenantMemberPageResponse documents pagination.Result[tenant.Member] for swag v1,
// which cannot resolve that generic type in an @Success annotation.
type TenantMemberPageResponse struct {
	Items    []tenant.Member `json:"items"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
	Total    int64           `json:"total"`
}

type UpdateTenantMemberStatusRequest struct {
	ID      string        `json:"id" binding:"required"`
	Status  tenant.Status `json:"status" binding:"required,oneof=active disabled"`
	Version int64         `json:"version" binding:"required,gt=0"`
}
type RemoveTenantMemberRequest struct {
	ID      string `json:"id" binding:"required"`
	Version int64  `json:"version" binding:"required,gt=0"`
}
type SwitchTenantRequest struct {
	TenantID string `json:"tenant_id" binding:"required"`
}

// Add godoc
// @Summary Add a user to the current tenant
// @Tags tenant-members
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body AddTenantMemberRequest true "Member"
// @Success 200 {object} Response{body=tenant.Member}
// @Router /api/v1/tenant-members/add [post]
func (h *TenantMemberHandler) Add(c *gin.Context) {
	var r AddTenantMemberRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Add(c.Request.Context(), r.Username)
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// Get godoc
// @Summary Get a tenant member
// @Tags tenant-members
// @Security Bearer
// @Param request body TenantMemberIDRequest true "Member"
// @Success 200 {object} Response{body=tenant.Member}
// @Router /api/v1/tenant-members/get [post]
func (h *TenantMemberHandler) Get(c *gin.Context) {
	var r TenantMemberIDRequest
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

// TenantMemberStatusTarget godoc
// @Summary Get a tenant member's current status and version for a status change
// @Tags tenant-members
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body TenantMemberIDRequest true "Member"
// @Success 200 {object} Response{body=tenant.MemberActionTarget}
// @Router /api/v1/tenant-members/status/target [post]
func (h *TenantMemberHandler) StatusTarget(c *gin.Context) {
	h.actionTarget(c)
}

// TenantMemberRemoveTarget godoc
// @Summary Get a tenant member's identity and version for removal confirmation
// @Tags tenant-members
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body TenantMemberIDRequest true "Member"
// @Success 200 {object} Response{body=tenant.MemberActionTarget}
// @Router /api/v1/tenant-members/remove-target [post]
func (h *TenantMemberHandler) RemoveTarget(c *gin.Context) {
	h.actionTarget(c)
}

func (h *TenantMemberHandler) actionTarget(c *gin.Context) {
	var request TenantMemberIDRequest
	if !h.bind(c, &request) {
		return
	}
	target, err := h.service.ActionTarget(c.Request.Context(), request.ID)
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, target)
}

// Page godoc
// @Summary Page current tenant members
// @Tags tenant-members
// @Security Bearer
// @Param request body TenantMemberPageRequest true "Filters"
// @Success 200 {object} Response{body=TenantMemberPageResponse}
// @Router /api/v1/tenant-members/page [post]
func (h *TenantMemberHandler) Page(c *gin.Context) {
	var r TenantMemberPageRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Page(c.Request.Context(), tenant.MemberPageInput{Request: pagination.Request{Page: r.Page, PageSize: r.PageSize, Keyword: r.Keyword}, IDs: r.IDs, UserIDs: r.UserIDs, Usernames: r.Usernames, Emails: r.Emails, Phones: r.Phones, Statuses: r.Statuses, JoinedFrom: r.JoinedFrom, JoinedTo: r.JoinedTo})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// UpdateStatus godoc
// @Summary Enable or disable a tenant member
// @Tags tenant-members
// @Security Bearer
// @Param request body UpdateTenantMemberStatusRequest true "Status"
// @Success 200 {object} Response{body=tenant.Member}
// @Router /api/v1/tenant-members/status/update [post]
func (h *TenantMemberHandler) UpdateStatus(c *gin.Context) {
	var r UpdateTenantMemberStatusRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.UpdateStatus(c.Request.Context(), r.ID, r.Status, r.Version)
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// Remove godoc
// @Summary Remove a tenant member
// @Tags tenant-members
// @Security Bearer
// @Param request body RemoveTenantMemberRequest true "Member"
// @Success 200 {object} Response
// @Router /api/v1/tenant-members/remove [post]
func (h *TenantMemberHandler) Remove(c *gin.Context) {
	var r RemoveTenantMemberRequest
	if !h.bind(c, &r) {
		return
	}
	if e := h.service.Remove(c.Request.Context(), r.ID, r.Version); e != nil {
		h.fail(c, e)
		return
	}
	OK(c, gin.H{})
}

// AvailableTenants godoc
// @Summary List the current user's available tenants
// @Tags tenant-context
// @Security Bearer
// @Success 200 {object} Response{body=[]tenant.AvailableTenant}
// @Router /api/v1/tenant-context/available [post]
func (h *TenantMemberHandler) AvailableTenants(c *gin.Context) {
	v, e := h.contexts.Available(c.Request.Context())
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// SwitchTenant godoc
// @Summary Issue an access token scoped to one tenant membership
// @Tags tenant-context
// @Security Bearer
// @Param request body SwitchTenantRequest true "Tenant"
// @Success 200 {object} Response{body=tenant.ContextToken}
// @Router /api/v1/tenant-context/switch [post]
func (h *TenantMemberHandler) SwitchTenant(c *gin.Context) {
	var r SwitchTenantRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.contexts.Switch(c.Request.Context(), r.TenantID)
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// CurrentTenant godoc
// @Summary Get the validated current tenant context
// @Tags tenant-context
// @Security Bearer
// @Success 200 {object} Response{body=tenant.AvailableTenant}
// @Router /api/v1/tenant-context/current [post]
func (h *TenantMemberHandler) CurrentTenant(c *gin.Context) {
	v, e := h.contexts.Current(c.Request.Context())
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}
func (h *TenantMemberHandler) bind(c *gin.Context, v any) bool {
	if e := c.ShouldBindJSON(v); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid tenant member request", e))
		return false
	}
	return true
}
func (h *TenantMemberHandler) fail(c *gin.Context, e error) {
	switch {
	case errors.Is(e, tenant.ErrContactFilterTooBroad):
		Fail(c, h.logger, apperror.Invalid("contact filter matches too many users; narrow the search", e))
	case errors.Is(e, tenant.ErrInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid tenant member request", e))
	case errors.Is(e, tenant.ErrForbidden):
		Fail(c, h.logger, apperror.Forbidden("tenant membership denied"))
	case errors.Is(e, tenant.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("tenant member not found"))
	case errors.Is(e, tenant.ErrStaleVersion):
		Fail(c, h.logger, apperror.StaleVersion(e))
	case errors.Is(e, tenant.ErrConflict):
		Fail(c, h.logger, apperror.Conflict("tenant member conflict", e))
	case errors.Is(e, tenant.ErrIdentityUnavailable):
		Fail(c, h.logger, apperror.Unavailable("identity service unavailable", e))
	case errors.Is(e, tenant.ErrSecurityUnavailable):
		Fail(c, h.logger, apperror.Unavailable("security audit is unavailable", e))
	default:
		Fail(c, h.logger, apperror.Internal(e))
	}
}
