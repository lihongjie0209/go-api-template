package httptransport

import (
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/dictionary"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
)

type DictionaryHandler struct {
	service *dictionary.Service
	logger  *slog.Logger
}

func NewDictionaryHandler(service *dictionary.Service, logger *slog.Logger) *DictionaryHandler {
	return &DictionaryHandler{service: service, logger: logger}
}

type CreateDictionaryRequest struct {
	Code        string            `json:"code" binding:"required,max=128"`
	Name        string            `json:"name" binding:"required,max=256"`
	Type        dictionary.Kind   `json:"type" binding:"required,oneof=enum tree"`
	Source      dictionary.Source `json:"source" binding:"required,oneof=static provider"`
	Description string            `json:"description" binding:"max=4096"`
	Status      string            `json:"status" binding:"required,oneof=active disabled"`
	Extension   json.RawMessage   `json:"extension" swaggertype:"object"`
}
type CreateDictionaryItemRequest struct {
	DictionaryID string          `json:"dictionary_id" binding:"required,uuid"`
	ParentID     *string         `json:"parent_id" binding:"omitempty,uuid"`
	Code         string          `json:"code" binding:"required,max=128"`
	Name         string          `json:"name" binding:"required,max=256"`
	Value        string          `json:"value" binding:"max=1048576"`
	Disabled     bool            `json:"disabled"`
	SortOrder    int64           `json:"sort_order"`
	Extension    json.RawMessage `json:"extension" swaggertype:"object"`
}
type DictionaryVersionRequest struct {
	ID      string `json:"id" binding:"required,uuid"`
	Version int64  `json:"version" binding:"required,gt=0"`
}
type UpdateDictionaryRequest struct {
	DictionaryVersionRequest
	Name        string          `json:"name" binding:"required,max=256"`
	Description string          `json:"description" binding:"max=4096"`
	Status      string          `json:"status" binding:"required,oneof=active disabled"`
	Extension   json.RawMessage `json:"extension" swaggertype:"object"`
}
type UpdateDictionaryItemRequest struct {
	DictionaryVersionRequest
	ParentID  *string         `json:"parent_id" binding:"omitempty,uuid"`
	Name      string          `json:"name" binding:"required,max=256"`
	Value     string          `json:"value" binding:"max=1048576"`
	Disabled  bool            `json:"disabled"`
	SortOrder int64           `json:"sort_order"`
	Extension json.RawMessage `json:"extension" swaggertype:"object"`
}
type DictionaryIDRequest struct {
	ID string `json:"id" binding:"required,uuid"`
}
type DictionaryPageRequest struct {
	pagination.Request
	Types    []dictionary.Kind   `json:"types" binding:"max=10,dive,oneof=enum tree"`
	Sources  []dictionary.Source `json:"sources" binding:"max=10,dive,oneof=static provider"`
	Statuses []string            `json:"statuses" binding:"max=10,dive,oneof=active disabled"`
}
type DictionaryItemPageRequest struct {
	pagination.Request
	DictionaryID string  `json:"dictionary_id" binding:"required,uuid"`
	Disabled     *bool   `json:"disabled"`
	ParentID     *string `json:"parent_id" binding:"omitempty,uuid"`
}

// QueryDictionary godoc
// @Summary Query a static or dynamic data dictionary
// @Description Public endpoint. Enum results are paged; tree results are returned as a bounded complete tree.
// @Tags data-dictionaries
// @Accept json
// @Produce json
// @Param request body dictionary.Query true "Dictionary query"
// @Success 200 {object} Response{body=dictionary.Result}
// @Router /api/v1/public/dictionaries/query [post]
func (h *DictionaryHandler) Query(c *gin.Context) {
	var r dictionary.Query
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Query(c.Request.Context(), r)
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// CreateDefinition godoc
// @Summary Create a data dictionary
// @Tags data-dictionaries
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CreateDictionaryRequest true "Dictionary"
// @Success 200 {object} Response{body=dictionary.Definition}
// @Router /api/v1/dictionaries/create [post]
func (h *DictionaryHandler) CreateDefinition(c *gin.Context) {
	var r CreateDictionaryRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.CreateDefinition(c.Request.Context(), dictionary.DefinitionInput{Code: r.Code, Name: r.Name, Kind: r.Type, Source: r.Source, Description: r.Description, Status: r.Status, Extension: r.Extension})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// GetDefinition godoc
// @Summary Get a data dictionary
// @Tags data-dictionaries
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DictionaryIDRequest true "Dictionary"
// @Success 200 {object} Response{body=dictionary.Definition}
// @Router /api/v1/dictionaries/get [post]
func (h *DictionaryHandler) GetDefinition(c *gin.Context) {
	var r DictionaryIDRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.GetDefinition(c.Request.Context(), r.ID)
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// PageDefinitions godoc
// @Summary Page data dictionaries
// @Tags data-dictionaries
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DictionaryPageRequest true "Filters"
// @Success 200 {object} Response{body=dictionary.Page}
// @Router /api/v1/dictionaries/page [post]
func (h *DictionaryHandler) PageDefinitions(c *gin.Context) {
	var r DictionaryPageRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.PageDefinitions(c.Request.Context(), dictionary.DefinitionPageInput{Request: r.Request, Types: r.Types, Sources: r.Sources, Statuses: r.Statuses})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// CreateItem godoc
// @Summary Create a static dictionary item
// @Tags data-dictionaries
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CreateDictionaryItemRequest true "Dictionary item"
// @Success 200 {object} Response{body=dictionary.Item}
// @Router /api/v1/dictionary-items/create [post]
func (h *DictionaryHandler) CreateItem(c *gin.Context) {
	var r CreateDictionaryItemRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.CreateItem(c.Request.Context(), dictionary.ItemInput{DictionaryID: r.DictionaryID, ParentID: r.ParentID, Code: r.Code, Name: r.Name, Value: r.Value, Disabled: r.Disabled, SortOrder: r.SortOrder, Extension: r.Extension})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// GetItem godoc
// @Summary Get a dictionary item
// @Tags data-dictionaries
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DictionaryIDRequest true "Item"
// @Success 200 {object} Response{body=dictionary.Item}
// @Router /api/v1/dictionary-items/get [post]
func (h *DictionaryHandler) GetItem(c *gin.Context) {
	var r DictionaryIDRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.GetItem(c.Request.Context(), r.ID)
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// PageItems godoc
// @Summary Page static dictionary items
// @Tags data-dictionaries
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DictionaryItemPageRequest true "Filters"
// @Success 200 {object} Response{body=dictionary.ItemPage}
// @Router /api/v1/dictionary-items/page [post]
func (h *DictionaryHandler) PageItems(c *gin.Context) {
	var r DictionaryItemPageRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.PageItems(c.Request.Context(), dictionary.ItemPageInput{Request: r.Request, DictionaryID: r.DictionaryID, Disabled: r.Disabled, ParentID: r.ParentID})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// UpdateDefinition godoc
// @Summary Update a data dictionary
// @Tags data-dictionaries
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body UpdateDictionaryRequest true "Dictionary"
// @Success 200 {object} Response{body=dictionary.Definition}
// @Router /api/v1/dictionaries/update [post]
func (h *DictionaryHandler) UpdateDefinition(c *gin.Context) {
	var r UpdateDictionaryRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.UpdateDefinition(c.Request.Context(), dictionary.DefinitionUpdate{ID: r.ID, Name: r.Name, Description: r.Description, Status: r.Status, Extension: r.Extension, Version: r.Version})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// DeleteDefinition godoc
// @Summary Delete an empty data dictionary
// @Tags data-dictionaries
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DictionaryVersionRequest true "Dictionary"
// @Success 200 {object} Response
// @Router /api/v1/dictionaries/delete [post]
func (h *DictionaryHandler) DeleteDefinition(c *gin.Context) {
	var r DictionaryVersionRequest
	if !h.bind(c, &r) {
		return
	}
	if e := h.service.DeleteDefinition(c.Request.Context(), r.ID, r.Version); e != nil {
		h.fail(c, e)
		return
	}
	OK(c, gin.H{})
}

// UpdateItem godoc
// @Summary Update a static dictionary item
// @Tags data-dictionaries
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body UpdateDictionaryItemRequest true "Item"
// @Success 200 {object} Response{body=dictionary.Item}
// @Router /api/v1/dictionary-items/update [post]
func (h *DictionaryHandler) UpdateItem(c *gin.Context) {
	var r UpdateDictionaryItemRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.UpdateItem(c.Request.Context(), dictionary.ItemUpdate{ID: r.ID, ParentID: r.ParentID, Name: r.Name, Value: r.Value, Disabled: r.Disabled, SortOrder: r.SortOrder, Extension: r.Extension, Version: r.Version})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// DeleteItem godoc
// @Summary Delete a leaf dictionary item
// @Tags data-dictionaries
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DictionaryVersionRequest true "Item"
// @Success 200 {object} Response
// @Router /api/v1/dictionary-items/delete [post]
func (h *DictionaryHandler) DeleteItem(c *gin.Context) {
	var r DictionaryVersionRequest
	if !h.bind(c, &r) {
		return
	}
	if e := h.service.DeleteItem(c.Request.Context(), r.ID, r.Version); e != nil {
		h.fail(c, e)
		return
	}
	OK(c, gin.H{})
}

func (h *DictionaryHandler) bind(c *gin.Context, v any) bool {
	if e := c.ShouldBindJSON(v); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid dictionary request", e))
		return false
	}
	return true
}
func (h *DictionaryHandler) fail(c *gin.Context, e error) {
	switch {
	case errors.Is(e, dictionary.ErrInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid dictionary request", e))
	case errors.Is(e, dictionary.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("dictionary not found"))
	case errors.Is(e, dictionary.ErrConflict):
		Fail(c, h.logger, apperror.Conflict("dictionary conflict", e))
	case errors.Is(e, dictionary.ErrProviderUnavailable):
		Fail(c, h.logger, apperror.Unavailable("dictionary provider unavailable", e))
	default:
		Fail(c, h.logger, apperror.Internal(e))
	}
}
