package httptransport

import (
	"log/slog"

	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/buildinfo"
	"github.com/lihongjie0209/go-api-template/internal/health"
	"github.com/lihongjie0209/go-api-template/internal/user"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	auth   *auth.Service
	logger *slog.Logger
	health *health.Service
	users  *user.Service
}

func NewHandler(authService *auth.Service, healthService *health.Service, userService *user.Service, logger *slog.Logger) *Handler {
	return &Handler{auth: authService, health: healthService, users: userService, logger: logger}
}

type LoginRequest struct {
	ClientID     string `json:"client_id" binding:"required" example:"local-client"`
	ClientSecret string `json:"client_secret" binding:"required" example:"local-secret"`
}
type LoginResponseBody struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}
type MeResponseBody struct {
	Subject string `json:"subject"`
}
type CreateUserRequest struct {
	Name  string `json:"name" binding:"required" example:"Alice"`
	Email string `json:"email" binding:"required" example:"alice@example.com"`
}
type GetUserRequest struct {
	ID string `json:"id" binding:"required" format:"uuid"`
}
type ListUsersRequest struct {
	Page     int `json:"page" example:"1"`
	PageSize int `json:"page_size" example:"20"`
}
type UpdateUserRequest struct {
	ID      string `json:"id" binding:"required" format:"uuid"`
	Name    string `json:"name" binding:"required"`
	Email   string `json:"email" binding:"required"`
	Version int64  `json:"version" binding:"required"`
}
type DeleteUserRequest struct {
	ID      string `json:"id" binding:"required" format:"uuid"`
	Version int64  `json:"version" binding:"required"`
}

// Login godoc
// @Summary Issue a JWT access token
// @Tags authentication
// @Accept json
// @Produce json
// @Param request body LoginRequest true "Client credentials"
// @Success 200 {object} Response{body=LoginResponseBody}
// @Failure 400 {object} Response "Code 10001: invalid request"
// @Failure 401 {object} Response "Code 20001: invalid credentials"
// @Failure 429 {object} Response "Code 10029: rate limited"
// @Router /api/v1/auth/login [post]
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	if !h.auth.Authenticate(req.ClientID, req.ClientSecret) {
		Fail(c, h.logger, apperror.Unauthorized("invalid credentials"))
		return
	}
	token, err := h.auth.Issue(req.ClientID)
	if err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, gin.H{"access_token": token, "token_type": "Bearer"})
}

// Live godoc
// @Summary Check process liveness
// @Tags operations
// @Produce json
// @Success 200 {object} Response{body=health.Status}
// @Router /live [post]
func (h *Handler) Live(c *gin.Context) { OK(c, h.health.Live()) }

// Ready godoc
// @Summary Check database and Redis readiness
// @Tags operations
// @Produce json
// @Success 200 {object} Response{body=health.Status}
// @Failure 503 {object} Response{body=health.Status} "Code 50003: dependency unavailable"
// @Router /ready [post]
func (h *Handler) Ready(c *gin.Context) {
	status, ready := h.health.Ready(c.Request.Context())
	if !ready {
		c.JSON(503, Response{Code: apperror.CodeDependencyUnavailable, Message: "service is not ready", Body: status, RequestID: requestID(c)})
		return
	}
	OK(c, status)
}

// Me godoc
// @Summary Return the authenticated subject
// @Tags authentication
// @Produce json
// @Security Bearer
// @Success 200 {object} Response{body=MeResponseBody}
// @Failure 401 {object} Response "Code 20001: unauthorized"
// @Router /api/v1/me [post]
func (h *Handler) Me(c *gin.Context) {
	subject, _ := c.Get("subject")
	OK(c, gin.H{"subject": subject})
}

// Version godoc
// @Summary Return build and runtime version information
// @Tags operations
// @Produce json
// @Success 200 {object} Response{body=buildinfo.Info}
// @Router /api/v1/version [post]
func (h *Handler) Version(c *gin.Context) { OK(c, buildinfo.Current()) }

// CreateUser godoc
// @Summary Create a user
// @Tags users
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CreateUserRequest true "User"
// @Success 200 {object} Response{body=user.User}
// @Failure 400 {object} Response "Code 10001: invalid request"
// @Failure 409 {object} Response "Code 30009: email already exists"
// @Router /api/v1/users/create [post]
func (h *Handler) CreateUser(c *gin.Context) {
	var req CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	created, err := h.users.Create(c.Request.Context(), req.Name, req.Email)
	if err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, created)
}

// GetUser godoc
// @Summary Get a user
// @Tags users
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body GetUserRequest true "User ID"
// @Success 200 {object} Response{body=user.User}
// @Failure 404 {object} Response "Code 10004: user not found"
// @Router /api/v1/users/get [post]
func (h *Handler) GetUser(c *gin.Context) {
	var req GetUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	found, err := h.users.Get(c.Request.Context(), req.ID)
	if err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, found)
}

// ListUsers godoc
// @Summary List users
// @Tags users
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body ListUsersRequest true "Pagination"
// @Success 200 {object} Response{body=user.Page}
// @Router /api/v1/users/list [post]
func (h *Handler) ListUsers(c *gin.Context) {
	var req ListUsersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	page, err := h.users.List(c.Request.Context(), req.Page, req.PageSize)
	if err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, page)
}

// UpdateUser godoc
// @Summary Update a user using optimistic locking
// @Tags users
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body UpdateUserRequest true "User and current version"
// @Success 200 {object} Response{body=user.User}
// @Failure 409 {object} Response "Code 30009: version conflict"
// @Router /api/v1/users/update [post]
func (h *Handler) UpdateUser(c *gin.Context) {
	var req UpdateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	updated, err := h.users.Update(c.Request.Context(), req.ID, req.Name, req.Email, req.Version)
	if err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, updated)
}

// DeleteUser godoc
// @Summary Delete a user using optimistic locking
// @Tags users
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DeleteUserRequest true "User ID and current version"
// @Success 200 {object} Response
// @Router /api/v1/users/delete [post]
func (h *Handler) DeleteUser(c *gin.Context) {
	var req DeleteUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	if err := h.users.Delete(c.Request.Context(), req.ID, req.Version); err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, gin.H{"deleted": true})
}
