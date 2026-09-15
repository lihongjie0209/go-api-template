package httptransport

import (
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/menu"
)

type MenuHandler struct {
	service *menu.Service
	logger  *slog.Logger
}

func NewMenuHandler(service *menu.Service, logger *slog.Logger) *MenuHandler {
	return &MenuHandler{service, logger}
}

type MenuMutationRequest struct {
	ID           string          `json:"id"`
	ParentID     *string         `json:"parent_id"`
	MenuKey      string          `json:"menu_key"`
	Name         string          `json:"name" binding:"required"`
	MenuType     string          `json:"menu_type" binding:"required"`
	RoutePath    string          `json:"route_path"`
	Component    string          `json:"component"`
	ExternalURL  string          `json:"external_url"`
	Icon         string          `json:"icon"`
	PermissionID *string         `json:"permission_id"`
	Visible      bool            `json:"visible"`
	Status       string          `json:"status" binding:"required"`
	SortOrder    int64           `json:"sort_order"`
	Metadata     json.RawMessage `json:"metadata" swaggertype:"object"`
	Version      int64           `json:"version"`
}
type MenuIDRequest struct {
	ID string `json:"id" binding:"required"`
}
type DeleteMenuRequest struct {
	ID      string `json:"id" binding:"required"`
	Version int64  `json:"version" binding:"required,gt=0"`
}
type MenuTreeRequest struct {
	Keyword       string     `json:"keyword"`
	IDs           []string   `json:"ids"`
	Types         []string   `json:"types"`
	Statuses      []string   `json:"statuses"`
	CreatedAtFrom *time.Time `json:"created_at_from"`
	CreatedAtTo   *time.Time `json:"created_at_to"`
}

// Create godoc
// @Summary Create a platform menu with a stable UUID v5 derived from menu_key
// @Tags menus
// @Security Bearer
// @Param request body MenuTreeRequest true "Tree filters"
// @Param request body MenuMutationRequest true "Menu"
// @Success 200 {object} Response{body=menu.Record}
// @Router /api/v1/menus/create [post]
func (h *MenuHandler) Create(c *gin.Context) {
	var r MenuMutationRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Create(c.Request.Context(), menu.Input{ParentID: r.ParentID, Key: r.MenuKey, Name: r.Name, Type: r.MenuType, RoutePath: r.RoutePath, Component: r.Component, ExternalURL: r.ExternalURL, Icon: r.Icon, PermissionID: r.PermissionID, Visible: r.Visible, Status: r.Status, SortOrder: r.SortOrder, Metadata: r.Metadata})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// Get godoc
// @Summary Get a platform menu
// @Tags menus
// @Security Bearer
// @Param request body MenuIDRequest true "Menu"
// @Success 200 {object} Response{body=menu.Record}
// @Router /api/v1/menus/get [post]
func (h *MenuHandler) Get(c *gin.Context) {
	var r MenuIDRequest
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

// Tree godoc
// @Summary Return the complete platform menu tree
// @Tags menus
// @Security Bearer
// @Success 200 {object} Response{body=[]menu.Node}
// @Router /api/v1/menus/tree [post]
func (h *MenuHandler) Tree(c *gin.Context) {
	var request MenuTreeRequest
	if !h.bind(c, &request) {
		return
	}
	v, e := h.service.Tree(c.Request.Context(), menu.TreeInput{Keyword: request.Keyword, IDs: request.IDs, Types: request.Types, Statuses: request.Statuses, CreatedAtFrom: request.CreatedAtFrom, CreatedAtTo: request.CreatedAtTo})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// Current godoc
// @Summary Return menus visible in the current tenant context
// @Tags menus
// @Security Bearer
// @Success 200 {object} Response{body=[]menu.Node}
// @Router /api/v1/me/menus [post]
func (h *MenuHandler) Current(c *gin.Context) {
	v, e := h.service.CurrentTree(c.Request.Context())
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// Update godoc
// @Summary Update or move a platform menu
// @Tags menus
// @Security Bearer
// @Param request body MenuMutationRequest true "Menu and version"
// @Success 200 {object} Response{body=menu.Record}
// @Router /api/v1/menus/update [post]
func (h *MenuHandler) Update(c *gin.Context) {
	var r MenuMutationRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Update(c.Request.Context(), menu.UpdateInput{ID: r.ID, ParentID: r.ParentID, Name: r.Name, Type: r.MenuType, RoutePath: r.RoutePath, Component: r.Component, ExternalURL: r.ExternalURL, Icon: r.Icon, PermissionID: r.PermissionID, Visible: r.Visible, Status: r.Status, SortOrder: r.SortOrder, Metadata: r.Metadata, Version: r.Version})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// Delete godoc
// @Summary Delete a leaf platform menu
// @Tags menus
// @Security Bearer
// @Param request body DeleteMenuRequest true "Menu and version"
// @Success 200 {object} Response
// @Router /api/v1/menus/delete [post]
func (h *MenuHandler) Delete(c *gin.Context) {
	var r DeleteMenuRequest
	if !h.bind(c, &r) {
		return
	}
	if e := h.service.Delete(c.Request.Context(), r.ID, r.Version); e != nil {
		h.fail(c, e)
		return
	}
	OK(c, gin.H{})
}
func (h *MenuHandler) bind(c *gin.Context, v any) bool {
	if e := c.ShouldBindJSON(v); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid menu request", e))
		return false
	}
	return true
}
func (h *MenuHandler) fail(c *gin.Context, e error) {
	switch {
	case errors.Is(e, menu.ErrInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid menu", e))
	case errors.Is(e, menu.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("menu not found"))
	case errors.Is(e, menu.ErrConflict):
		Fail(c, h.logger, apperror.Conflict("menu conflict", e))
	default:
		Fail(c, h.logger, apperror.Internal(e))
	}
}
