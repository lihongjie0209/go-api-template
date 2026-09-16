package httptransport

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type DataPermissionHandler struct {
	service *datapermission.LifecycleService
	logger  *slog.Logger
}

func NewDataPermissionHandler(service *datapermission.LifecycleService, logger *slog.Logger) *DataPermissionHandler {
	return &DataPermissionHandler{service: service, logger: logger}
}

type DataPermissionPolicyCreateRequest struct {
	Policy datapermission.Policy `json:"policy" binding:"required"`
}
type DataPermissionPolicyGetRequest struct {
	ID string `json:"id" binding:"required,uuid"`
}
type DataPermissionPolicyPageRequest struct {
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
	Keyword  string   `json:"keyword" binding:"omitempty,max=256"`
	Statuses []string `json:"statuses" binding:"max=2,dive,oneof=active disabled"`
}
type DataPermissionVersionCreateRequest struct {
	PolicyID              string                `json:"policy_id" binding:"required,uuid"`
	ExpectedPolicyVersion int64                 `json:"expected_policy_version" binding:"required,gt=0"`
	Policy                datapermission.Policy `json:"policy" binding:"required"`
}
type DataPermissionVersionGetRequest struct {
	PolicyID      string `json:"policy_id" binding:"required,uuid"`
	VersionNumber int64  `json:"version_number" binding:"required,gt=0"`
}
type DataPermissionVersionPageRequest struct {
	PolicyID string   `json:"policy_id" binding:"required,uuid"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
	Statuses []string `json:"statuses" binding:"max=3,dive,oneof=draft published archived"`
}
type DataPermissionPublishRequest struct {
	PolicyID              string `json:"policy_id" binding:"required,uuid"`
	VersionNumber         int64  `json:"version_number" binding:"required,gt=0"`
	ExpectedPolicyVersion int64  `json:"expected_policy_version" binding:"required,gt=0"`
}
type DataPermissionStatusRequest struct {
	PolicyID              string `json:"policy_id" binding:"required,uuid"`
	Status                string `json:"status" binding:"required,oneof=active disabled"`
	ExpectedPolicyVersion int64  `json:"expected_policy_version" binding:"required,gt=0"`
}

// CreateDataPermissionPolicy godoc
// @Summary Create a global or tenant data-permission policy draft
// @Tags data-permission
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DataPermissionPolicyCreateRequest true "Policy"
// @Success 200 {object} Response{body=datapermission.Publication}
// @Router /api/v1/data-permissions/global-policies/create [post]
// @Router /api/v1/data-permissions/tenant-policies/create [post]
func (h *DataPermissionHandler) Create(c *gin.Context) {
	var request DataPermissionPolicyCreateRequest
	if !h.bind(c, &request) {
		return
	}
	if request.Policy.Scope.Type != expectedDataPolicyScope(c) {
		h.respond(c, nil, datapermission.ErrPolicyScope)
		return
	}
	policy, version, err := h.service.Create(c.Request.Context(), request.Policy)
	h.respond(c, datapermission.Publication{Policy: policy, Version: version}, err)
}

// GetDataPermissionPolicy godoc
// @Summary Get a data-permission policy
// @Tags data-permission
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DataPermissionPolicyGetRequest true "Policy ID"
// @Success 200 {object} Response{body=datapermission.PolicyRecord}
// @Router /api/v1/data-permissions/global-policies/get [post]
// @Router /api/v1/data-permissions/tenant-policies/get [post]
func (h *DataPermissionHandler) Get(c *gin.Context) {
	var request DataPermissionPolicyGetRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.Get(c.Request.Context(), request.ID)
	if err == nil && result.Scope != expectedDataPolicyScope(c) {
		err = datapermission.ErrPolicyNotFound
	}
	h.respond(c, result, err)
}

// PageDataPermissionPolicies godoc
// @Summary Page data-permission policies
// @Tags data-permission
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DataPermissionPolicyPageRequest true "Filters"
// @Success 200 {object} Response{body=datapermission.PolicyPage}
// @Router /api/v1/data-permissions/global-policies/page [post]
// @Router /api/v1/data-permissions/tenant-policies/page [post]
func (h *DataPermissionHandler) Page(c *gin.Context) {
	var request DataPermissionPolicyPageRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.Page(c.Request.Context(), datapermission.PolicyPageInput{
		Request: pagination.Request{Page: request.Page, PageSize: request.PageSize, Keyword: request.Keyword},
		Scopes:  []datapermission.PolicyScopeType{expectedDataPolicyScope(c)}, Statuses: request.Statuses,
	})
	h.respond(c, result, err)
}

// CreateDataPermissionVersion godoc
// @Summary Append an immutable data-permission policy draft
// @Tags data-permission
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DataPermissionVersionCreateRequest true "Draft"
// @Success 200 {object} Response{body=datapermission.VersionRecord}
// @Router /api/v1/data-permissions/global-policies/versions/create [post]
// @Router /api/v1/data-permissions/tenant-policies/versions/create [post]
func (h *DataPermissionHandler) CreateVersion(c *gin.Context) {
	var request DataPermissionVersionCreateRequest
	if !h.bind(c, &request) {
		return
	}
	if request.Policy.Scope.Type != expectedDataPolicyScope(c) {
		h.respond(c, nil, datapermission.ErrPolicyScope)
		return
	}
	result, err := h.service.CreateVersion(c.Request.Context(), request.PolicyID, request.ExpectedPolicyVersion, request.Policy)
	h.respond(c, result, err)
}

// GetDataPermissionVersion godoc
// @Summary Get one data-permission policy version
// @Tags data-permission
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DataPermissionVersionGetRequest true "Version"
// @Success 200 {object} Response{body=datapermission.VersionRecord}
// @Router /api/v1/data-permissions/global-policies/versions/get [post]
// @Router /api/v1/data-permissions/tenant-policies/versions/get [post]
func (h *DataPermissionHandler) GetVersion(c *gin.Context) {
	var request DataPermissionVersionGetRequest
	if !h.bind(c, &request) {
		return
	}
	if !h.policyMatches(c, request.PolicyID) {
		return
	}
	result, err := h.service.GetVersion(c.Request.Context(), request.PolicyID, request.VersionNumber)
	h.respond(c, result, err)
}

// PageDataPermissionVersions godoc
// @Summary Page immutable data-permission policy versions
// @Tags data-permission
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DataPermissionVersionPageRequest true "Filters"
// @Success 200 {object} Response{body=datapermission.VersionPage}
// @Router /api/v1/data-permissions/global-policies/versions/page [post]
// @Router /api/v1/data-permissions/tenant-policies/versions/page [post]
func (h *DataPermissionHandler) PageVersions(c *gin.Context) {
	var request DataPermissionVersionPageRequest
	if !h.bind(c, &request) {
		return
	}
	if !h.policyMatches(c, request.PolicyID) {
		return
	}
	result, err := h.service.PageVersions(c.Request.Context(), datapermission.VersionPageInput{PolicyID: request.PolicyID, Request: pagination.Request{Page: request.Page, PageSize: request.PageSize}, Statuses: request.Statuses})
	h.respond(c, result, err)
}

// PublishDataPermissionPolicy godoc
// @Summary Atomically publish a data-permission policy draft
// @Tags data-permission
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DataPermissionPublishRequest true "Publication"
// @Success 200 {object} Response{body=datapermission.PolicyRecord}
// @Router /api/v1/data-permissions/global-policies/publish [post]
// @Router /api/v1/data-permissions/tenant-policies/publish [post]
func (h *DataPermissionHandler) Publish(c *gin.Context) {
	var request DataPermissionPublishRequest
	if !h.bind(c, &request) || !h.policyMatches(c, request.PolicyID) {
		return
	}
	result, err := h.service.Publish(c.Request.Context(), request.PolicyID, request.VersionNumber, request.ExpectedPolicyVersion)
	h.respond(c, result, err)
}

// SetDataPermissionPolicyStatus godoc
// @Summary Enable or disable a data-permission policy
// @Tags data-permission
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DataPermissionStatusRequest true "Status"
// @Success 200 {object} Response{body=datapermission.PolicyRecord}
// @Router /api/v1/data-permissions/global-policies/status/set [post]
// @Router /api/v1/data-permissions/tenant-policies/status/set [post]
func (h *DataPermissionHandler) SetStatus(c *gin.Context) {
	var request DataPermissionStatusRequest
	if !h.bind(c, &request) || !h.policyMatches(c, request.PolicyID) {
		return
	}
	result, err := h.service.SetStatus(c.Request.Context(), datapermission.SetPolicyStatusInput{PolicyID: request.PolicyID, Status: request.Status, ExpectedPolicyVersion: request.ExpectedPolicyVersion})
	h.respond(c, result, err)
}

func (h *DataPermissionHandler) policyMatches(c *gin.Context, id string) bool {
	policy, err := h.service.Get(c.Request.Context(), id)
	if err == nil && policy.Scope != expectedDataPolicyScope(c) {
		err = datapermission.ErrPolicyNotFound
	}
	if err != nil {
		h.respond(c, nil, err)
		return false
	}
	return true
}

func expectedDataPolicyScope(c *gin.Context) datapermission.PolicyScopeType {
	if strings.Contains(c.FullPath(), "/tenant-policies/") {
		return datapermission.PolicyScopeTenant
	}
	return datapermission.PolicyScopeGlobal
}

func (h *DataPermissionHandler) bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", err))
		return false
	}
	return true
}

func (h *DataPermissionHandler) respond(c *gin.Context, body any, err error) {
	if err == nil {
		OK(c, body)
		return
	}
	switch {
	case errors.Is(err, datapermission.ErrInvalidPolicy), errors.Is(err, datapermission.ErrInvalidPredicate), errors.Is(err, datapermission.ErrInvalidSchema):
		Fail(c, h.logger, apperror.Invalid("invalid data-permission policy", err))
	case errors.Is(err, datapermission.ErrPolicyNotFound):
		Fail(c, h.logger, apperror.NotFound("data-permission policy not found"))
	case errors.Is(err, datapermission.ErrPolicyConflict), errors.Is(err, datapermission.ErrVersionNotDraft):
		Fail(c, h.logger, apperror.Conflict("data-permission policy state conflict", err))
	case errors.Is(err, datapermission.ErrPolicyScope):
		Fail(c, h.logger, apperror.Forbidden("data-permission policy scope denied"))
	case errors.Is(err, platformprincipal.ErrMissing):
		Fail(c, h.logger, apperror.Unauthorized("authentication required"))
	default:
		Fail(c, h.logger, apperror.Internal(err))
	}
}
