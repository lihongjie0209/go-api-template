package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/navigation"
)

type NavigationHandler struct {
	service      *navigation.Service
	capabilities navigationCapabilityEvaluator
	logger       *slog.Logger
}

type navigationCapabilityEvaluator interface {
	Evaluate(context.Context, []authorization.CapabilityRequest) (authorization.CapabilityResult, error)
}

func NewNavigationHandler(service *navigation.Service, capabilities *authorization.CapabilityService, logger *slog.Logger) *NavigationHandler {
	return &NavigationHandler{service: service, capabilities: capabilities, logger: logger}
}

type CreateNavigationRequest struct {
	ApplicationID string          `json:"application_id" binding:"required,max=128"`
	ParentID      *string         `json:"parent_id" binding:"omitempty,max=128"`
	Key           string          `json:"navigation_key" binding:"required,max=128"`
	Name          string          `json:"name" binding:"required,max=256"`
	Type          string          `json:"navigation_type" binding:"required,oneof=directory menu"`
	RoutePath     string          `json:"route_path" binding:"max=2048"`
	Component     string          `json:"component" binding:"max=512"`
	Icon          string          `json:"icon" binding:"max=256"`
	Resource      string          `json:"resource" binding:"max=128"`
	Action        string          `json:"action" binding:"max=128"`
	Visible       bool            `json:"visible"`
	Status        string          `json:"status" binding:"required,oneof=active disabled"`
	SortOrder     int64           `json:"sort_order" binding:"gte=-1000000000,lte=1000000000"`
	Metadata      json.RawMessage `json:"metadata" swaggertype:"object"`
}
type NavigationIDRequest struct {
	ID string `json:"id" binding:"required,max=128"`
}
type NavigationTreeRequest struct {
	ApplicationID string   `json:"application_id" binding:"required,max=128"`
	Keyword       string   `json:"keyword" binding:"max=256"`
	Types         []string `json:"types" binding:"max=2,dive,oneof=directory menu"`
	Statuses      []string `json:"statuses" binding:"max=2,dive,oneof=active disabled"`
}
type CurrentNavigationRequest struct {
	ApplicationID string `json:"application_id" binding:"required,max=128"`
}
type UpdateNavigationRequest struct {
	ID        string          `json:"id" binding:"required,max=128"`
	ParentID  *string         `json:"parent_id" binding:"omitempty,max=128"`
	Name      string          `json:"name" binding:"required,max=256"`
	Type      string          `json:"navigation_type" binding:"required,oneof=directory menu"`
	RoutePath string          `json:"route_path" binding:"max=2048"`
	Component string          `json:"component" binding:"max=512"`
	Icon      string          `json:"icon" binding:"max=256"`
	Resource  string          `json:"resource" binding:"max=128"`
	Action    string          `json:"action" binding:"max=128"`
	Visible   bool            `json:"visible"`
	Status    string          `json:"status" binding:"required,oneof=active disabled"`
	SortOrder int64           `json:"sort_order" binding:"gte=-1000000000,lte=1000000000"`
	Metadata  json.RawMessage `json:"metadata" swaggertype:"object"`
	Version   int64           `json:"version" binding:"required,gt=0"`
}
type DeleteNavigationRequest struct {
	ID      string `json:"id" binding:"required,max=128"`
	Version int64  `json:"version" binding:"required,gt=0"`
}

// CreateNavigation godoc
// @Summary Create an application navigation
// @Tags navigations
// @Security Bearer
// @Param request body CreateNavigationRequest true "Navigation"
// @Success 200 {object} Response{body=navigation.Record}
// @Router /api/v1/navigations/create [post]
func (h *NavigationHandler) Create(c *gin.Context) {
	var r CreateNavigationRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Create(c.Request.Context(), navigation.Input{ApplicationID: r.ApplicationID, ParentID: r.ParentID, Key: r.Key, Name: r.Name, Type: r.Type, RoutePath: r.RoutePath, Component: r.Component, Icon: r.Icon, Resource: r.Resource, Action: r.Action, Visible: r.Visible, Status: r.Status, SortOrder: r.SortOrder, Metadata: r.Metadata})
	h.respond(c, v, e)
}

// GetNavigation godoc
// @Summary Get an application navigation
// @Tags navigations
// @Security Bearer
// @Param request body NavigationIDRequest true "Navigation"
// @Success 200 {object} Response{body=navigation.Record}
// @Router /api/v1/navigations/get [post]
func (h *NavigationHandler) Get(c *gin.Context) {
	var r NavigationIDRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Get(c.Request.Context(), r.ID)
	h.respond(c, v, e)
}

// TreeNavigations godoc
// @Summary Get one application's navigation tree
// @Tags navigations
// @Security Bearer
// @Param request body NavigationTreeRequest true "Filters"
// @Success 200 {object} Response{body=[]navigation.Node}
// @Router /api/v1/navigations/tree [post]
func (h *NavigationHandler) Tree(c *gin.Context) {
	var r NavigationTreeRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Tree(c.Request.Context(), navigation.TreeInput{ApplicationID: r.ApplicationID, Keyword: r.Keyword, Types: r.Types, Statuses: r.Statuses})
	h.respond(c, v, e)
}

// CurrentNavigations godoc
// @Summary Get the current principal's visible application navigation
// @Tags navigations
// @Security Bearer
// @Param request body CurrentNavigationRequest true "Application"
// @Success 200 {object} Response{body=[]navigation.Node}
// @Router /api/v1/me/navigations [post]
func (h *NavigationHandler) Current(c *gin.Context) {
	var request CurrentNavigationRequest
	if !h.bind(c, &request) {
		return
	}
	tree, err := h.service.Tree(c.Request.Context(), navigation.TreeInput{ApplicationID: request.ApplicationID, Statuses: []string{"active"}})
	if err != nil {
		h.respond(c, nil, err)
		return
	}
	requests := make([]authorization.CapabilityRequest, 0)
	collectNavigationCapabilities(tree, &requests)
	allowed := make(map[string]struct{}, len(requests))
	for start := 0; start < len(requests); start += authorization.MaxCapabilityItems {
		end := min(start+authorization.MaxCapabilityItems, len(requests))
		result, evaluateErr := h.capabilities.Evaluate(c.Request.Context(), requests[start:end])
		if evaluateErr != nil {
			Fail(c, h.logger, apperror.AuthorizationUnavailable(evaluateErr))
			return
		}
		for _, decision := range result.Items {
			if decision.Allowed {
				allowed[decision.Key] = struct{}{}
			}
		}
	}
	h.respond(c, navigation.PruneVisible(tree, allowed), nil)
}

func collectNavigationCapabilities(nodes []*navigation.Node, result *[]authorization.CapabilityRequest) {
	for _, node := range nodes {
		if node.Type == "menu" && node.Visible && node.Status == "active" {
			*result = append(*result, authorization.CapabilityRequest{Key: node.ID, Resource: node.Resource, Action: node.Action})
		}
		collectNavigationCapabilities(node.Children, result)
	}
}

// UpdateNavigation godoc
// @Summary Update or move an application navigation
// @Tags navigations
// @Security Bearer
// @Param request body UpdateNavigationRequest true "Navigation"
// @Success 200 {object} Response{body=navigation.Record}
// @Router /api/v1/navigations/update [post]
func (h *NavigationHandler) Update(c *gin.Context) {
	var r UpdateNavigationRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Update(c.Request.Context(), navigation.UpdateInput{ID: r.ID, ParentID: r.ParentID, Name: r.Name, Type: r.Type, RoutePath: r.RoutePath, Component: r.Component, Icon: r.Icon, Resource: r.Resource, Action: r.Action, Visible: r.Visible, Status: r.Status, SortOrder: r.SortOrder, Metadata: r.Metadata, Version: r.Version})
	h.respond(c, v, e)
}

// DeleteNavigation godoc
// @Summary Delete a leaf application navigation
// @Tags navigations
// @Security Bearer
// @Param request body DeleteNavigationRequest true "Navigation"
// @Success 200 {object} Response
// @Router /api/v1/navigations/delete [post]
func (h *NavigationHandler) Delete(c *gin.Context) {
	var r DeleteNavigationRequest
	if !h.bind(c, &r) {
		return
	}
	h.respond(c, gin.H{}, h.service.Delete(c.Request.Context(), r.ID, r.Version))
}
func (h *NavigationHandler) bind(c *gin.Context, v any) bool {
	if e := c.ShouldBindJSON(v); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid navigation request", e))
		return false
	}
	return true
}
func (h *NavigationHandler) respond(c *gin.Context, v any, e error) {
	switch {
	case e == nil:
		OK(c, v)
	case errors.Is(e, navigation.ErrInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid navigation", e))
	case errors.Is(e, navigation.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("navigation not found"))
	case errors.Is(e, navigation.ErrConflict):
		Fail(c, h.logger, apperror.Conflict("navigation conflict", e))
	default:
		Fail(c, h.logger, apperror.Internal(e))
	}
}
