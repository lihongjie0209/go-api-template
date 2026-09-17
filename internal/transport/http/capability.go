package httptransport

import (
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/authorization"
)

type CapabilityHandler struct {
	service *authorization.CapabilityService
	logger  *slog.Logger
}

func NewCapabilityHandler(service *authorization.CapabilityService, logger *slog.Logger) *CapabilityHandler {
	return &CapabilityHandler{service: service, logger: logger}
}

type CapabilityEvaluateRequest struct {
	Items []authorization.CapabilityRequest `json:"items" binding:"required,min=1,max=100,dive"`
}

type RowCapabilityEvaluateRequest struct {
	Resource    string   `json:"resource" binding:"required,max=128"`
	Actions     []string `json:"actions" binding:"required,min=1,max=20,dive,required,max=128"`
	ResourceIDs []string `json:"resource_ids" binding:"required,min=1,max=200,dive,required,max=256"`
}

// EvaluateCapabilities godoc
// @Summary Batch evaluate page-level capabilities for the current principal
// @Tags authorization
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CapabilityEvaluateRequest true "Capabilities"
// @Success 200 {object} Response{body=[]authorization.CapabilityDecision}
// @Router /api/v1/authorization/capabilities/evaluate [post]
func (h *CapabilityHandler) Evaluate(c *gin.Context) {
	var request CapabilityEvaluateRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.Evaluate(c.Request.Context(), request.Items)
	h.respond(c, result, err)
}

// EvaluateRowCapabilities godoc
// @Summary Batch evaluate current-row capabilities for the current principal
// @Tags authorization
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body RowCapabilityEvaluateRequest true "Rows"
// @Success 200 {object} Response{body=authorization.RowCapabilityResult}
// @Router /api/v1/authorization/rows/evaluate [post]
func (h *CapabilityHandler) EvaluateRows(c *gin.Context) {
	var request RowCapabilityEvaluateRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.EvaluateRows(c.Request.Context(), request.Resource, request.Actions, request.ResourceIDs)
	h.respond(c, result, err)
}

func (h *CapabilityHandler) bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid capability request", err))
		return false
	}
	return true
}

func (h *CapabilityHandler) respond(c *gin.Context, result any, err error) {
	switch {
	case err == nil:
		OK(c, result)
	case errors.Is(err, authorization.ErrCapabilityInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid capability request", err))
	case errors.Is(err, authorization.ErrCapabilityUnavailable):
		Fail(c, h.logger, apperror.AuthorizationUnavailable(err))
	default:
		Fail(c, h.logger, apperror.AuthorizationUnavailable(err))
	}
}
