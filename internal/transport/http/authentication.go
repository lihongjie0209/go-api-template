package httptransport

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type AuthenticationHandler struct {
	auth     *auth.Service
	security securitylog.Recorder
	logger   *slog.Logger
}

func NewAuthenticationHandler(authService *auth.Service, securityRecorder securitylog.Recorder, logger *slog.Logger) *AuthenticationHandler {
	return &AuthenticationHandler{auth: authService, security: securityRecorder, logger: logger}
}

type LoginRequest struct {
	ClientID     string `json:"client_id" binding:"required,max=256"`
	ClientSecret string `json:"client_secret" binding:"required,max=4096"`
}
type LoginResponseBody struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// Login godoc
// @Summary Issue a JWT access token and record a security event
// @Tags authentication
// @Accept json
// @Produce json
// @Param request body LoginRequest true "Client credentials"
// @Success 200 {object} Response{body=LoginResponseBody}
// @Failure 400 {object} Response
// @Failure 401 {object} Response
// @Failure 429 {object} Response
// @Failure 503 {object} Response
// @Router /api/v1/auth/login [post]
func (h *AuthenticationHandler) Login(c *gin.Context) {
	var request LoginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid login request", err))
		return
	}
	entry := securitylog.Entry{EventType: securitylog.EventLogin, Identifier: request.ClientID, SubjectType: string(platformprincipal.TypeServiceAccount), ClientIP: c.ClientIP(), UserAgent: c.Request.UserAgent()}
	if !h.auth.Authenticate(request.ClientID, request.ClientSecret) {
		entry.Succeeded = false
		entry.Reason = "invalid_credentials"
		entry.ErrorCode = "invalid_credentials"
		if err := h.security.Record(c.Request.Context(), entry); err != nil {
			h.logger.ErrorContext(c.Request.Context(), "record failed login security event", "error", err, "request_id", requestID(c))
		}
		Fail(c, h.logger, apperror.Unauthorized("invalid credentials"))
		return
	}
	token, err := h.auth.Issue(request.ClientID)
	if err != nil {
		Fail(c, h.logger, apperror.Internal(err))
		return
	}
	claims, err := h.auth.Parse(token)
	if err != nil {
		Fail(c, h.logger, apperror.Internal(err))
		return
	}
	entry.SubjectID = request.ClientID
	entry.TokenID = claims.ID
	entry.Succeeded = true
	if err := h.security.Record(c.Request.Context(), entry); err != nil {
		if h.security.FailClosed() {
			Fail(c, h.logger, apperror.Unavailable("security audit is unavailable", err))
			return
		}
		h.logger.ErrorContext(c.Request.Context(), "record successful login security event", "error", err, "request_id", requestID(c))
	}
	OK(c, LoginResponseBody{AccessToken: token, TokenType: "Bearer"})
}
