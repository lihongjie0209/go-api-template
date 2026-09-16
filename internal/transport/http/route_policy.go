package httptransport

import (
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/routepolicy"
)

type RoutePolicyHandler struct {
	service *routepolicy.Service
	logger  *slog.Logger
}

func NewRoutePolicyHandler(service *routepolicy.Service, logger *slog.Logger) *RoutePolicyHandler {
	return &RoutePolicyHandler{service: service, logger: logger}
}

type routePolicyGetRequest struct {
	RouteID string `json:"route_id" binding:"required,max=128"`
}

type routePolicySetRequest struct {
	RouteID     string                       `json:"route_id" binding:"required,max=128"`
	Expression  string                       `json:"expression" binding:"required,max=4096"`
	Description string                       `json:"description" binding:"max=4096"`
	Status      string                       `json:"status" binding:"required,oneof=active disabled"`
	Version     int64                        `json:"version" binding:"gte=0"`
	References  []routepolicy.ReferenceInput `json:"references" binding:"max=8,dive"`
}

type routePolicyPageRequest struct {
	Page      int      `json:"page"`
	PageSize  int      `json:"page_size"`
	Keyword   string   `json:"keyword" binding:"omitempty,max=256"`
	Protocols []string `json:"protocols" binding:"max=20,dive,oneof=http grpc"`
	Statuses  []string `json:"statuses" binding:"max=20,dive,oneof=active inactive"`
}

// PageRoutePolicies godoc
// @Summary Page discovered HTTP and gRPC routes with their policy status
// @Tags route-policies
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body routePolicyPageRequest true "Filters"
// @Success 200 {object} Response{body=routepolicy.Page}
// @Router /api/v1/route-policies/page [post]
func (h *RoutePolicyHandler) Page(c *gin.Context) {
	var request routePolicyPageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", err))
		return
	}
	result, err := h.service.Page(c.Request.Context(), routepolicy.PageInput{
		Request:   pagination.Request{Page: request.Page, PageSize: request.PageSize, Keyword: request.Keyword},
		Protocols: request.Protocols, Statuses: request.Statuses,
	})
	if err != nil {
		if errors.Is(err, routepolicy.ErrInvalid) {
			Fail(c, h.logger, apperror.Invalid("invalid route policy filters", err))
			return
		}
		Fail(c, h.logger, apperror.Internal(err))
		return
	}
	OK(c, result)
}

// GetRoutePolicy godoc
// @Summary Get the database-owned policy for a route
// @Tags route-policies
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body routePolicyGetRequest true "Route"
// @Success 200 {object} Response{body=routepolicy.View}
// @Router /api/v1/route-policies/get [post]
func (h *RoutePolicyHandler) Get(c *gin.Context) {
	var request routePolicyGetRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", err))
		return
	}
	record, err := h.service.Get(c.Request.Context(), request.RouteID)
	if err != nil {
		if errors.Is(err, routepolicy.ErrNotFound) {
			Fail(c, h.logger, apperror.NotFound("route policy not found"))
			return
		}
		Fail(c, h.logger, apperror.Internal(err))
		return
	}
	OK(c, record)
}

// SetRoutePolicy godoc
// @Summary Create or update a database-owned route policy
// @Tags route-policies
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body routePolicySetRequest true "Policy"
// @Success 200 {object} Response{body=routepolicy.View}
// @Router /api/v1/route-policies/set [post]
func (h *RoutePolicyHandler) Set(c *gin.Context) {
	var request routePolicySetRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", err))
		return
	}
	record, err := h.service.Set(c.Request.Context(), routepolicy.SetInput{
		RouteID: request.RouteID, Expression: request.Expression, Description: request.Description,
		Status: request.Status, Version: request.Version, References: request.References,
	})
	if err != nil {
		switch {
		case errors.Is(err, routepolicy.ErrInvalid):
			Fail(c, h.logger, apperror.Invalid("invalid route policy", err))
		case errors.Is(err, routepolicy.ErrNotFound):
			Fail(c, h.logger, apperror.NotFound("route not found"))
		case errors.Is(err, routepolicy.ErrConflict):
			Fail(c, h.logger, apperror.Conflict("route policy version conflict", err))
		default:
			Fail(c, h.logger, apperror.Internal(err))
		}
		return
	}
	OK(c, record)
}
