package httptransport

import (
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
)

type UserHandler struct {
	service *identity.Service
	logger  *slog.Logger
}

func NewUserHandler(service *identity.Service, logger *slog.Logger) *UserHandler {
	return &UserHandler{service, logger}
}

type CreateUserRequest struct {
	Username    string `json:"username" binding:"required"`
	DisplayName string `json:"display_name" binding:"required"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
}
type GetUserRequest struct {
	ID string `json:"id" binding:"required"`
}
type PageUsersRequest struct {
	Page          int               `json:"page"`
	PageSize      int               `json:"page_size"`
	Keyword       string            `json:"keyword"`
	IDs           []string          `json:"ids" binding:"max=200"`
	Usernames     []string          `json:"usernames" binding:"max=200"`
	Emails        []string          `json:"emails" binding:"max=200"`
	Phones        []string          `json:"phones" binding:"max=200"`
	Statuses      []identity.Status `json:"statuses" binding:"max=20,dive,oneof=active disabled locked closed"`
	CreatedAtFrom *time.Time        `json:"created_at_from"`
	CreatedAtTo   *time.Time        `json:"created_at_to"`
}
type UpdateUserRequest struct {
	ID          string          `json:"id" binding:"required"`
	DisplayName string          `json:"display_name" binding:"required"`
	Email       string          `json:"email"`
	Phone       string          `json:"phone"`
	Status      identity.Status `json:"status" binding:"required"`
	Version     int64           `json:"version" binding:"required,gt=0"`
}
type DeleteUserRequest struct {
	ID      string `json:"id" binding:"required"`
	Version int64  `json:"version" binding:"required,gt=0"`
}
type UpdateProfileRequest struct {
	DisplayName string `json:"display_name" binding:"required"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	Version     int64  `json:"version" binding:"required,gt=0"`
}

// GetProfile godoc
// @Summary Get the authenticated user's profile
// @Tags profile
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body object true "Empty JSON object"
// @Success 200 {object} Response{body=identity.User}
// @Router /api/v1/profile/get [post]
func (h *UserHandler) GetProfile(c *gin.Context) {
	var request struct{}
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.Self(c.Request.Context())
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, result)
}

// UpdateProfile godoc
// @Summary Update the authenticated user's profile with optimistic locking
// @Tags profile
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body UpdateProfileRequest true "Profile and expected version"
// @Success 200 {object} Response{body=identity.User}
// @Router /api/v1/profile/update [post]
func (h *UserHandler) UpdateProfile(c *gin.Context) {
	var request UpdateProfileRequest
	if !h.bind(c, &request) {
		return
	}
	result, err := h.service.UpdateSelf(c.Request.Context(), identity.SelfUpdateInput{
		DisplayName: request.DisplayName, Email: request.Email,
		Phone: request.Phone, Version: request.Version,
	})
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, result)
}

// CreateUser godoc
// @Summary Create a globally identified user
// @Tags users
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CreateUserRequest true "User"
// @Success 200 {object} Response{body=identity.User}
// @Router /api/v1/users/create [post]
func (h *UserHandler) Create(c *gin.Context) {
	var r CreateUserRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Create(c.Request.Context(), identity.CreateInput{Username: r.Username, DisplayName: r.DisplayName, Email: r.Email, Phone: r.Phone})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// GetUser godoc
// @Summary Get a user by ID
// @Tags users
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body GetUserRequest true "User ID"
// @Success 200 {object} Response{body=identity.User}
// @Router /api/v1/users/get [post]
func (h *UserHandler) Get(c *gin.Context) {
	var r GetUserRequest
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

// PageUsers godoc
// @Summary Search users by username, email, phone, status, and time range
// @Tags users
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body PageUsersRequest true "User filters"
// @Success 200 {object} Response
// @Router /api/v1/users/page [post]
func (h *UserHandler) Page(c *gin.Context) {
	var r PageUsersRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Page(c.Request.Context(), identity.PageInput{Request: pagination.Request{Page: r.Page, PageSize: r.PageSize, Keyword: r.Keyword}, IDs: r.IDs, Usernames: r.Usernames, Emails: r.Emails, Phones: r.Phones, Statuses: r.Statuses, CreatedAtFrom: r.CreatedAtFrom, CreatedAtTo: r.CreatedAtTo})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// UpdateUser godoc
// @Summary Update a user using optimistic locking
// @Tags users
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body UpdateUserRequest true "User and version"
// @Success 200 {object} Response{body=identity.User}
// @Router /api/v1/users/update [post]
func (h *UserHandler) Update(c *gin.Context) {
	var r UpdateUserRequest
	if !h.bind(c, &r) {
		return
	}
	v, e := h.service.Update(c.Request.Context(), identity.UpdateInput{ID: r.ID, DisplayName: r.DisplayName, Email: r.Email, Phone: r.Phone, Status: r.Status, Version: r.Version})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, v)
}

// DeleteUser godoc
// @Summary Logically close and delete a user
// @Tags users
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DeleteUserRequest true "User and version"
// @Success 200 {object} Response
// @Router /api/v1/users/delete [post]
func (h *UserHandler) Delete(c *gin.Context) {
	var r DeleteUserRequest
	if !h.bind(c, &r) {
		return
	}
	if e := h.service.Delete(c.Request.Context(), r.ID, r.Version); e != nil {
		h.fail(c, e)
		return
	}
	OK(c, gin.H{})
}
func (h *UserHandler) bind(c *gin.Context, v any) bool {
	if e := c.ShouldBindJSON(v); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", e))
		return false
	}
	return true
}
func (h *UserHandler) fail(c *gin.Context, e error) {
	switch {
	case errors.Is(e, identity.ErrInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid user", e))
	case errors.Is(e, identity.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("user not found"))
	case errors.Is(e, identity.ErrConflict), errors.Is(e, identity.ErrInUse):
		Fail(c, h.logger, apperror.Conflict("username or user version conflict", e))
	case errors.Is(e, identity.ErrForbidden):
		Fail(c, h.logger, apperror.Forbidden("user operation forbidden"))
	default:
		Fail(c, h.logger, apperror.Internal(e))
	}
}
