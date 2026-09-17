package httptransport

import (
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type PBACHandler struct {
	service   *pbac.LifecycleService
	registry  *pbac.Registry
	simulator *pbac.Simulator
	logger    *slog.Logger
}

func NewPBACHandler(service *pbac.LifecycleService, registry *pbac.Registry, simulator *pbac.Simulator, logger *slog.Logger) *PBACHandler {
	return &PBACHandler{service: service, registry: registry, simulator: simulator, logger: logger}
}

type PBACResourceListRequest struct {
	Scope pbac.ResourceScope `json:"scope" binding:"omitempty,oneof=platform tenant principal"`
}

type PBACPolicyCreateRequest struct {
	Policy pbac.Policy `json:"policy" binding:"required"`
}
type PBACPolicyGetRequest struct {
	ID string `json:"id" binding:"required,uuid"`
}
type PBACPolicyPageRequest struct {
	Page          int        `json:"page"`
	PageSize      int        `json:"page_size"`
	Keyword       string     `json:"keyword" binding:"omitempty,max=256"`
	IDs           []string   `json:"ids" binding:"max=200,dive,uuid"`
	Codes         []string   `json:"codes" binding:"max=200,dive,max=128"`
	Statuses      []string   `json:"statuses" binding:"max=2,dive,oneof=active disabled"`
	CreatedAtFrom *time.Time `json:"created_at_from"`
	CreatedAtTo   *time.Time `json:"created_at_to"`
	UpdatedAtFrom *time.Time `json:"updated_at_from"`
	UpdatedAtTo   *time.Time `json:"updated_at_to"`
}
type PBACPolicyVersionCreateRequest struct {
	PolicyID              string      `json:"policy_id" binding:"required,uuid"`
	ExpectedPolicyVersion int64       `json:"expected_policy_version" binding:"required,gt=0"`
	Policy                pbac.Policy `json:"policy" binding:"required"`
}
type PBACPolicyVersionGetRequest struct {
	PolicyID      string `json:"policy_id" binding:"required,uuid"`
	VersionNumber int64  `json:"version_number" binding:"required,gt=0"`
}
type PBACPolicyVersionPageRequest struct {
	PolicyID string   `json:"policy_id" binding:"required,uuid"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
	Statuses []string `json:"statuses" binding:"max=3,dive,oneof=draft published archived"`
}
type PBACPolicyPublishRequest struct {
	PolicyID              string `json:"policy_id" binding:"required,uuid"`
	VersionNumber         int64  `json:"version_number" binding:"required,gt=0"`
	ExpectedPolicyVersion int64  `json:"expected_policy_version" binding:"required,gt=0"`
}
type PBACPolicyStatusSetRequest struct {
	PolicyID              string `json:"policy_id" binding:"required,uuid"`
	Status                string `json:"status" binding:"required,oneof=active disabled"`
	ExpectedPolicyVersion int64  `json:"expected_policy_version" binding:"required,gt=0"`
}

type PBACSimulationRequest struct {
	Policy  pbac.Policy            `json:"policy" binding:"required"`
	Request pbac.EvaluationRequest `json:"request" binding:"required"`
}

// SimulatePBACPolicy godoc
// @Summary Validate and simulate one unpersisted PBAC policy
// @Tags pbac
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PBACSimulationRequest true "Simulation"
// @Success 200 {object} Response{body=pbac.Decision}
// @Router /api/v1/pbac/global-policies/simulate [post]
// @Router /api/v1/pbac/tenant-policies/simulate [post]
func (h *PBACHandler) Simulate(c *gin.Context) {
	var request PBACSimulationRequest
	if !h.bind(c, &request) {
		return
	}
	if request.Policy.Scope.Type != expectedPBACPolicyScope(c) || !simulationTenantAllowed(c, request.Policy.Scope.TenantID, request.Request.Subject.TenantID, request.Request.Resource.TenantID) {
		h.respond(c, nil, pbac.ErrTenantAccessDenied)
		return
	}
	result, err := h.simulator.Simulate(c.Request.Context(), pbac.SimulationInput{Policy: request.Policy, Request: request.Request})
	h.respond(c, result, err)
}

// ListPBACResources godoc
// @Summary List registered PBAC resources and actions
// @Tags pbac
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PBACResourceListRequest true "Filter"
// @Success 200 {object} Response{body=[]pbac.ResourceDefinition}
// @Router /api/v1/pbac/resources/list [post]
func (h *PBACHandler) ListResources(c *gin.Context) {
	var request PBACResourceListRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", err))
		return
	}
	definitions := h.registry.Definitions()
	if request.Scope != "" {
		filtered := make([]pbac.ResourceDefinition, 0, len(definitions))
		for _, definition := range definitions {
			if definition.Scope == request.Scope {
				filtered = append(filtered, definition)
			}
		}
		definitions = filtered
	}
	OK(c, definitions)
}

// CreatePBACPolicy godoc
// @Summary Create a PBAC policy and its first draft
// @Tags pbac
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PBACPolicyCreateRequest true "Policy"
// @Success 200 {object} Response{body=pbac.Publication}
// @Router /api/v1/pbac/global-policies/create [post]
// @Router /api/v1/pbac/tenant-policies/create [post]
func (h *PBACHandler) Create(c *gin.Context) {
	var request PBACPolicyCreateRequest
	if !h.bind(c, &request) {
		return
	}
	if request.Policy.Scope.Type != expectedPBACPolicyScope(c) {
		h.respond(c, nil, pbac.ErrTenantAccessDenied)
		return
	}
	result, err := h.service.Create(c.Request.Context(), pbac.CreatePolicyInput{Document: request.Policy})
	h.respond(c, result, err)
}

// GetPBACPolicy godoc
// @Summary Get a PBAC policy
// @Tags pbac
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PBACPolicyGetRequest true "Policy ID"
// @Success 200 {object} Response{body=pbac.PolicyRecord}
// @Router /api/v1/pbac/global-policies/get [post]
// @Router /api/v1/pbac/tenant-policies/get [post]
func (h *PBACHandler) Get(c *gin.Context) {
	var request PBACPolicyGetRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.Get(c.Request.Context(), request.ID)
	if err == nil && result.Scope != expectedPBACPolicyScope(c) {
		err = pbac.ErrPolicyNotFound
	}
	h.respond(c, result, err)
}

// PagePBACPolicies godoc
// @Summary Page PBAC policies visible to the current tenant context
// @Tags pbac
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PBACPolicyPageRequest true "Filters"
// @Success 200 {object} Response{body=pbac.PolicyPage}
// @Router /api/v1/pbac/global-policies/page [post]
// @Router /api/v1/pbac/tenant-policies/page [post]
func (h *PBACHandler) Page(c *gin.Context) {
	var request PBACPolicyPageRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.Page(c.Request.Context(), pbac.PolicyPageInput{Request: pagination.Request{Page: request.Page, PageSize: request.PageSize, Keyword: request.Keyword}, IDs: request.IDs, Codes: request.Codes, Scopes: []pbac.PolicyScopeType{expectedPBACPolicyScope(c)}, Statuses: request.Statuses, CreatedAtFrom: request.CreatedAtFrom, CreatedAtTo: request.CreatedAtTo, UpdatedAtFrom: request.UpdatedAtFrom, UpdatedAtTo: request.UpdatedAtTo})
	h.respond(c, result, err)
}

// CreatePBACPolicyVersion godoc
// @Summary Append an immutable PBAC policy draft
// @Tags pbac
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PBACPolicyVersionCreateRequest true "Draft"
// @Success 200 {object} Response{body=pbac.PolicyVersionRecord}
// @Router /api/v1/pbac/global-policies/versions/create [post]
// @Router /api/v1/pbac/tenant-policies/versions/create [post]
func (h *PBACHandler) CreateVersion(c *gin.Context) {
	var request PBACPolicyVersionCreateRequest
	if !h.bind(c, &request) {
		return
	}
	if request.Policy.Scope.Type != expectedPBACPolicyScope(c) {
		h.respond(c, nil, pbac.ErrTenantAccessDenied)
		return
	}
	result, err := h.service.CreateVersion(c.Request.Context(), pbac.CreateVersionInput{PolicyID: request.PolicyID, ExpectedPolicyVersion: request.ExpectedPolicyVersion, Document: request.Policy})
	h.respond(c, result, err)
}

// GetPBACPolicyVersion godoc
// @Summary Get one PBAC policy version
// @Tags pbac
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PBACPolicyVersionGetRequest true "Version"
// @Success 200 {object} Response{body=pbac.PolicyVersionRecord}
// @Router /api/v1/pbac/global-policies/versions/get [post]
// @Router /api/v1/pbac/tenant-policies/versions/get [post]
func (h *PBACHandler) GetVersion(c *gin.Context) {
	var request PBACPolicyVersionGetRequest
	if !h.bind(c, &request) {
		return
	}
	policy, err := h.service.Get(c.Request.Context(), request.PolicyID)
	if err == nil && policy.Scope != expectedPBACPolicyScope(c) {
		err = pbac.ErrPolicyNotFound
	}
	var result pbac.PolicyVersionRecord
	if err == nil {
		result, err = h.service.GetVersion(c.Request.Context(), request.PolicyID, request.VersionNumber)
	}
	h.respond(c, result, err)
}

// PagePBACPolicyVersions godoc
// @Summary Page immutable versions of a PBAC policy
// @Tags pbac
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PBACPolicyVersionPageRequest true "Filters"
// @Success 200 {object} Response{body=pbac.PolicyVersionPage}
// @Router /api/v1/pbac/global-policies/versions/page [post]
// @Router /api/v1/pbac/tenant-policies/versions/page [post]
func (h *PBACHandler) PageVersions(c *gin.Context) {
	var request PBACPolicyVersionPageRequest
	if !h.bind(c, &request) {
		return
	}
	policy, err := h.service.Get(c.Request.Context(), request.PolicyID)
	if err == nil && policy.Scope != expectedPBACPolicyScope(c) {
		err = pbac.ErrPolicyNotFound
	}
	var result pbac.PolicyVersionPage
	if err == nil {
		result, err = h.service.PageVersions(c.Request.Context(), pbac.PolicyVersionPageInput{PolicyID: request.PolicyID, Request: pagination.Request{Page: request.Page, PageSize: request.PageSize}, Statuses: request.Statuses})
	}
	h.respond(c, result, err)
}

// PublishPBACPolicy godoc
// @Summary Atomically publish a PBAC policy draft
// @Tags pbac
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PBACPolicyPublishRequest true "Publication"
// @Success 200 {object} Response{body=pbac.Publication}
// @Router /api/v1/pbac/global-policies/publish [post]
// @Router /api/v1/pbac/tenant-policies/publish [post]
func (h *PBACHandler) Publish(c *gin.Context) {
	var request PBACPolicyPublishRequest
	if !h.bind(c, &request) {
		return
	}
	policy, err := h.service.Get(c.Request.Context(), request.PolicyID)
	if err == nil && policy.Scope != expectedPBACPolicyScope(c) {
		err = pbac.ErrPolicyNotFound
	}
	var result pbac.Publication
	if err == nil {
		result, err = h.service.Publish(c.Request.Context(), pbac.PublishInput{PolicyID: request.PolicyID, VersionNumber: request.VersionNumber, ExpectedPolicyVersion: request.ExpectedPolicyVersion})
	}
	h.respond(c, result, err)
}

// SetPBACPolicyStatus godoc
// @Summary Enable or disable a PBAC policy
// @Tags pbac
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PBACPolicyStatusSetRequest true "Status"
// @Success 200 {object} Response{body=pbac.PolicyRecord}
// @Router /api/v1/pbac/global-policies/status/set [post]
// @Router /api/v1/pbac/tenant-policies/status/set [post]
func (h *PBACHandler) SetStatus(c *gin.Context) {
	var request PBACPolicyStatusSetRequest
	if !h.bind(c, &request) {
		return
	}
	policy, err := h.service.Get(c.Request.Context(), request.PolicyID)
	if err == nil && policy.Scope != expectedPBACPolicyScope(c) {
		err = pbac.ErrPolicyNotFound
	}
	var result pbac.PolicyRecord
	if err == nil {
		result, err = h.service.SetStatus(c.Request.Context(), pbac.SetPolicyStatusInput{PolicyID: request.PolicyID, Status: request.Status, ExpectedPolicyVersion: request.ExpectedPolicyVersion})
	}
	h.respond(c, result, err)
}

func expectedPBACPolicyScope(c *gin.Context) pbac.PolicyScopeType {
	if strings.Contains(c.FullPath(), "/tenant-policies/") {
		return pbac.PolicyScopeTenant
	}
	return pbac.PolicyScopeGlobal
}

func (h *PBACHandler) bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", err))
		return false
	}
	return true
}
func (h *PBACHandler) respond(c *gin.Context, body any, err error) {
	if err == nil {
		OK(c, body)
		return
	}
	switch {
	case errors.Is(err, pbac.ErrInvalidPolicy), errors.Is(err, pbac.ErrInvalidEvaluationRequest):
		Fail(c, h.logger, apperror.Invalid("invalid PBAC policy", err))
	case errors.Is(err, pbac.ErrPolicyNotFound), errors.Is(err, pbac.ErrPolicyVersionMissing):
		Fail(c, h.logger, apperror.NotFound("PBAC policy not found"))
	case errors.Is(err, pbac.ErrPolicyConflict), errors.Is(err, pbac.ErrVersionNotDraft), errors.Is(err, pbac.ErrPolicyDisabled):
		Fail(c, h.logger, apperror.Conflict("PBAC policy state conflict", err))
	case errors.Is(err, pbac.ErrTenantAccessDenied):
		Fail(c, h.logger, apperror.Forbidden("tenant policy access denied"))
	case errors.Is(err, platformprincipal.ErrMissing):
		Fail(c, h.logger, apperror.Unauthorized("authentication required"))
	default:
		Fail(c, h.logger, apperror.Internal(err))
	}
}

func simulationTenantAllowed(c *gin.Context, tenantIDs ...string) bool {
	if !strings.Contains(c.FullPath(), "/tenant-policies/") {
		return true
	}
	principal, err := platformprincipal.Require(c.Request.Context())
	if err != nil || principal.TenantID == "" {
		return false
	}
	for _, tenantID := range tenantIDs {
		if tenantID != principal.TenantID {
			return false
		}
	}
	return true
}
