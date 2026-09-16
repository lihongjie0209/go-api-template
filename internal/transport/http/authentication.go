package httptransport

import (
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/serviceaccount"
)

type AuthenticationHandler struct {
	auth     *auth.Service
	accounts *serviceaccount.Service
	logger   *slog.Logger
}

func NewAuthenticationHandler(authService *auth.Service, accounts *serviceaccount.Service, logger *slog.Logger) *AuthenticationHandler {
	return &AuthenticationHandler{auth: authService, accounts: accounts, logger: logger}
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
	_, token, authenticateErr := h.accounts.AuthenticateAndIssue(c.Request.Context(), request.ClientID, request.ClientSecret, func(accountID string) (string, string, error) {
		issued, err := h.auth.Issue(accountID)
		if err != nil {
			return "", "", err
		}
		claims, err := h.auth.Parse(issued)
		if err != nil {
			return "", "", err
		}
		return issued, claims.ID, nil
	})
	if authenticateErr != nil {
		if errors.Is(authenticateErr, serviceaccount.ErrSecurityUnavailable) {
			Fail(c, h.logger, apperror.Unavailable("security audit is unavailable", authenticateErr))
			return
		}
		Fail(c, h.logger, apperror.Unauthorized("invalid credentials"))
		return
	}
	OK(c, LoginResponseBody{AccessToken: token, TokenType: "Bearer"})
}
