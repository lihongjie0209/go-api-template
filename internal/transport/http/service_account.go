package httptransport

import (
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/serviceaccount"
)

type ServiceAccountHandler struct {
	service *serviceaccount.Service
	logger  *slog.Logger
}

func NewServiceAccountHandler(service *serviceaccount.Service, logger *slog.Logger) *ServiceAccountHandler {
	return &ServiceAccountHandler{service: service, logger: logger}
}

type CreateServiceAccountRequest struct {
	ClientID    string     `json:"client_id" binding:"required"`
	Name        string     `json:"name" binding:"required"`
	Description string     `json:"description"`
	ExpiresAt   *time.Time `json:"expires_at"`
}
type GetServiceAccountRequest struct {
	ID string `json:"id" binding:"required"`
}
type PageServiceAccountsRequest struct {
	Page          int                     `json:"page"`
	PageSize      int                     `json:"page_size"`
	Keyword       string                  `json:"keyword"`
	IDs           []string                `json:"ids" binding:"max=200"`
	ClientIDs     []string                `json:"client_ids" binding:"max=200"`
	Statuses      []serviceaccount.Status `json:"statuses" binding:"max=20,dive,oneof=active disabled"`
	CreatedAtFrom *time.Time              `json:"created_at_from"`
	CreatedAtTo   *time.Time              `json:"created_at_to"`
	ExpiresAtFrom *time.Time              `json:"expires_at_from"`
	ExpiresAtTo   *time.Time              `json:"expires_at_to"`
}
type UpdateServiceAccountRequest struct {
	ID          string                `json:"id" binding:"required"`
	Name        string                `json:"name" binding:"required"`
	Description string                `json:"description"`
	Status      serviceaccount.Status `json:"status" binding:"required,oneof=active disabled"`
	ExpiresAt   *time.Time            `json:"expires_at"`
	Version     int64                 `json:"version" binding:"required,gt=0"`
}
type MutateServiceAccountSecretRequest struct {
	ID      string `json:"id" binding:"required"`
	Version int64  `json:"version" binding:"required,gt=0"`
}

// CreateServiceAccount godoc
// @Summary Create a service account and return its secret once
// @Tags service-accounts
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CreateServiceAccountRequest true "Service account"
// @Success 200 {object} Response{body=serviceaccount.Created}
// @Router /api/v1/service-accounts/create [post]
func (h *ServiceAccountHandler) Create(c *gin.Context) {
	var request CreateServiceAccountRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.Create(c.Request.Context(), serviceaccount.CreateInput{ClientID: request.ClientID, Name: request.Name, Description: request.Description, ExpiresAt: request.ExpiresAt})
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, result)
}

// GetServiceAccount godoc
// @Summary Get service account metadata without its secret hash
// @Tags service-accounts
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body GetServiceAccountRequest true "Service account ID"
// @Success 200 {object} Response{body=serviceaccount.Account}
// @Router /api/v1/service-accounts/get [post]
func (h *ServiceAccountHandler) Get(c *gin.Context) {
	var request GetServiceAccountRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.Get(c.Request.Context(), request.ID)
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, result)
}

// PageServiceAccounts godoc
// @Summary Search service accounts by identity, status, and time ranges
// @Tags service-accounts
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PageServiceAccountsRequest true "Service account filters"
// @Success 200 {object} Response
// @Router /api/v1/service-accounts/page [post]
func (h *ServiceAccountHandler) Page(c *gin.Context) {
	var request PageServiceAccountsRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.Page(c.Request.Context(), serviceaccount.PageInput{Request: pagination.Request{Page: request.Page, PageSize: request.PageSize, Keyword: request.Keyword}, IDs: request.IDs, ClientIDs: request.ClientIDs, Statuses: request.Statuses, CreatedAtFrom: request.CreatedAtFrom, CreatedAtTo: request.CreatedAtTo, ExpiresAtFrom: request.ExpiresAtFrom, ExpiresAtTo: request.ExpiresAtTo})
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, result)
}

// UpdateServiceAccount godoc
// @Summary Update a service account using optimistic locking
// @Tags service-accounts
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body UpdateServiceAccountRequest true "Service account and version"
// @Success 200 {object} Response{body=serviceaccount.Account}
// @Router /api/v1/service-accounts/update [post]
func (h *ServiceAccountHandler) Update(c *gin.Context) {
	var request UpdateServiceAccountRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.Update(c.Request.Context(), serviceaccount.UpdateInput{ID: request.ID, Name: request.Name, Description: request.Description, Status: request.Status, ExpiresAt: request.ExpiresAt, Version: request.Version})
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, result)
}

// RotateServiceAccountSecret godoc
// @Summary Rotate a service account secret and return it once
// @Tags service-accounts
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body MutateServiceAccountSecretRequest true "Service account and version"
// @Success 200 {object} Response{body=serviceaccount.Created}
// @Router /api/v1/service-accounts/secret/rotate [post]
func (h *ServiceAccountHandler) RotateSecret(c *gin.Context) {
	var request MutateServiceAccountSecretRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.RotateSecret(c.Request.Context(), request.ID, request.Version)
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, result)
}

// DeleteServiceAccount godoc
// @Summary Disable and logically delete a service account
// @Tags service-accounts
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body MutateServiceAccountSecretRequest true "Service account and version"
// @Success 200 {object} Response
// @Router /api/v1/service-accounts/delete [post]
func (h *ServiceAccountHandler) Delete(c *gin.Context) {
	var request MutateServiceAccountSecretRequest
	if !h.bind(c, &request) {
		return
	}
	if err := h.service.Delete(c.Request.Context(), request.ID, request.Version); err != nil {
		h.fail(c, err)
		return
	}
	OK(c, gin.H{})
}

func (h *ServiceAccountHandler) bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid service account request", err))
		return false
	}
	return true
}
func (h *ServiceAccountHandler) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, serviceaccount.ErrInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid service account", err))
	case errors.Is(err, serviceaccount.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("service account not found"))
	case errors.Is(err, serviceaccount.ErrConflict):
		Fail(c, h.logger, apperror.Conflict("service account identity or version conflict", err))
	default:
		Fail(c, h.logger, apperror.Internal(err))
	}
}
