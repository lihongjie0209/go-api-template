package httptransport

import (
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/authentication"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type UserAuthenticationHandler struct {
	service  *authentication.Service
	security securitylog.Recorder
	logger   *slog.Logger
}

func NewUserAuthenticationHandler(service *authentication.Service, security securitylog.Recorder, logger *slog.Logger) *UserAuthenticationHandler {
	return &UserAuthenticationHandler{service, security, logger}
}

type UserLoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required,max=1024"`
}
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required,max=4096"`
}
type SetPasswordRequest struct {
	UserID   string `json:"user_id" binding:"required"`
	Password string `json:"password" binding:"required,max=1024"`
}
type ChangePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required,max=1024"`
	NewPassword string `json:"new_password" binding:"required,max=1024"`
}
type SessionPageRequest struct {
	pagination.Request
	IDs            []string                       `json:"ids"`
	Statuses       []authentication.SessionStatus `json:"statuses"`
	ClientIPs      []string                       `json:"client_ips"`
	CreatedAtFrom  *time.Time                     `json:"created_at_from"`
	CreatedAtTo    *time.Time                     `json:"created_at_to"`
	LastSeenAtFrom *time.Time                     `json:"last_seen_at_from"`
	LastSeenAtTo   *time.Time                     `json:"last_seen_at_to"`
}
type RevokeSessionRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	Version   int64  `json:"version" binding:"required,gt=0"`
}
type ForceLogoutAllRequest struct {
	UserID string `json:"user_id" binding:"required"`
}

// UserLogin godoc
// @Summary Authenticate a user with username and password
// @Tags authentication
// @Accept json
// @Produce json
// @Param request body UserLoginRequest true "Credentials"
// @Success 200 {object} Response{body=authentication.Tokens}
// @Router /api/v1/auth/user/login [post]
func (h *UserAuthenticationHandler) Login(c *gin.Context) {
	var r UserLoginRequest
	if e := c.ShouldBindJSON(&r); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid login request", e))
		return
	}
	tokens, e := h.service.Login(c.Request.Context(), r.Username, r.Password, c.ClientIP(), c.Request.UserAgent())
	entry := securitylog.Entry{EventType: securitylog.EventLogin, Identifier: r.Username, SubjectType: string(platformprincipal.TypeUser), Succeeded: e == nil, ClientIP: c.ClientIP(), UserAgent: c.Request.UserAgent()}
	if e != nil {
		entry.Reason = loginFailureReason(e)
		if !errors.Is(e, authentication.ErrAttemptAudited) && !errors.Is(e, authentication.ErrSecurityUnavailable) {
			if logErr := h.record(c, entry); logErr != nil {
				h.logger.ErrorContext(c.Request.Context(), "record failed user login", "error", logErr, "request_id", requestID(c))
			}
		}
		h.fail(c, e)
		return
	}
	entry.SessionID = tokens.SessionID
	entry.SubjectID = tokens.UserID
	OK(c, tokens)
}

// RefreshToken godoc
// @Summary Rotate a refresh token and issue new tokens
// @Tags authentication
// @Accept json
// @Produce json
// @Param request body RefreshRequest true "Refresh token"
// @Success 200 {object} Response{body=authentication.Tokens}
// @Router /api/v1/auth/refresh [post]
func (h *UserAuthenticationHandler) Refresh(c *gin.Context) {
	var r RefreshRequest
	if e := c.ShouldBindJSON(&r); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid refresh request", e))
		return
	}
	tokens, e := h.service.Refresh(c.Request.Context(), r.RefreshToken)
	entry := securitylog.Entry{EventType: securitylog.EventTokenRefresh, SubjectID: tokens.UserID, SubjectType: string(platformprincipal.TypeUser), SessionID: tokens.SessionID, TokenID: r.RefreshToken, Succeeded: e == nil}
	if e != nil {
		if !errors.Is(e, authentication.ErrRefreshReused) {
			if logErr := h.record(c, entry); logErr != nil {
				h.logger.ErrorContext(c.Request.Context(), "record failed token refresh", "error", logErr, "request_id", requestID(c))
			}
		}
		h.fail(c, e)
		return
	}
	OK(c, tokens)
}

// UserLogout godoc
// @Summary Revoke a user session
// @Tags authentication
// @Accept json
// @Produce json
// @Param request body RefreshRequest true "Refresh token"
// @Success 200 {object} Response
// @Router /api/v1/auth/logout [post]
func (h *UserAuthenticationHandler) Logout(c *gin.Context) {
	var r RefreshRequest
	if e := c.ShouldBindJSON(&r); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid logout request", e))
		return
	}
	e := h.service.Logout(c.Request.Context(), r.RefreshToken)
	if e != nil {
		h.recordFailure(c, e, securitylog.Entry{EventType: securitylog.EventLogout, TokenID: r.RefreshToken}, "logout")
		h.fail(c, e)
		return
	}
	OK(c, gin.H{})
}

// SetUserPassword godoc
// @Summary Provision a user's password credential
// @Tags authentication
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body SetPasswordRequest true "User and password"
// @Success 200 {object} Response
// @Router /api/v1/auth/password/reset [post]
func (h *UserAuthenticationHandler) SetPassword(c *gin.Context) {
	var r SetPasswordRequest
	if e := c.ShouldBindJSON(&r); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid password request", e))
		return
	}
	e := h.service.SetPassword(c.Request.Context(), r.UserID, r.Password)
	if e != nil {
		h.recordFailure(c, e, securitylog.Entry{EventType: securitylog.EventPasswordReset, SubjectID: r.UserID, SubjectType: string(platformprincipal.TypeUser)}, "password reset")
		h.fail(c, e)
		return
	}
	OK(c, gin.H{})
}

// ChangePassword godoc
// @Summary Change the current user's password and revoke all sessions
// @Tags authentication
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body ChangePasswordRequest true "Passwords"
// @Success 200 {object} Response
// @Router /api/v1/auth/password/change [post]
func (h *UserAuthenticationHandler) ChangePassword(c *gin.Context) {
	var r ChangePasswordRequest
	if e := c.ShouldBindJSON(&r); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid password request", e))
		return
	}
	e := h.service.ChangePassword(c.Request.Context(), r.OldPassword, r.NewPassword)
	if e != nil {
		h.recordFailure(c, e, securitylog.Entry{EventType: securitylog.EventPasswordChanged}, "password change")
		h.fail(c, e)
		return
	}
	OK(c, gin.H{})
}

// Sessions godoc
// @Summary Page the current user's sessions
// @Tags sessions
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body SessionPageRequest true "Pagination"
// @Success 200 {object} Response{body=authentication.SessionPage}
// @Router /api/v1/auth/sessions/page [post]
func (h *UserAuthenticationHandler) Sessions(c *gin.Context) {
	var r SessionPageRequest
	if e := c.ShouldBindJSON(&r); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid session request", e))
		return
	}
	page, e := h.service.Sessions(c.Request.Context(), authentication.SessionPageInput{
		Request: r.Request, IDs: r.IDs, Statuses: r.Statuses, ClientIPs: r.ClientIPs,
		CreatedAtFrom: r.CreatedAtFrom, CreatedAtTo: r.CreatedAtTo, LastSeenAtFrom: r.LastSeenAtFrom, LastSeenAtTo: r.LastSeenAtTo,
	})
	if e != nil {
		h.fail(c, e)
		return
	}
	OK(c, page)
}

// RevokeSession godoc
// @Summary Revoke one of the current user's sessions
// @Tags sessions
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body RevokeSessionRequest true "Session"
// @Success 200 {object} Response
// @Router /api/v1/auth/sessions/revoke [post]
func (h *UserAuthenticationHandler) RevokeSession(c *gin.Context) {
	var r RevokeSessionRequest
	if e := c.ShouldBindJSON(&r); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid session request", e))
		return
	}
	e := h.service.RevokeSession(c.Request.Context(), r.SessionID, r.Version)
	if e != nil {
		h.recordFailure(c, e, securitylog.Entry{EventType: securitylog.EventSessionRevoked, SessionID: r.SessionID}, "session revoke")
		h.fail(c, e)
		return
	}
	OK(c, gin.H{})
}

// LogoutAll godoc
// @Summary Revoke all sessions for the current user
// @Tags sessions
// @Produce json
// @Security Bearer
// @Success 200 {object} Response
// @Router /api/v1/auth/sessions/logout-all [post]
func (h *UserAuthenticationHandler) LogoutAll(c *gin.Context) {
	e := h.service.LogoutAll(c.Request.Context())
	if e != nil {
		h.recordFailure(c, e, securitylog.Entry{EventType: securitylog.EventLogoutAll}, "logout all")
		h.fail(c, e)
		return
	}
	OK(c, gin.H{})
}

// ForceLogoutAll godoc
// @Summary Administratively revoke all sessions for a user
// @Tags sessions
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body ForceLogoutAllRequest true "User"
// @Success 200 {object} Response
// @Router /api/v1/auth/sessions/force-logout-all [post]
func (h *UserAuthenticationHandler) ForceLogoutAll(c *gin.Context) {
	var r ForceLogoutAllRequest
	if e := c.ShouldBindJSON(&r); e != nil {
		Fail(c, h.logger, apperror.Invalid("invalid session request", e))
		return
	}
	e := h.service.ForceLogoutAll(c.Request.Context(), r.UserID)
	if e != nil {
		h.recordFailure(c, e, securitylog.Entry{EventType: securitylog.EventForcedLogout, SubjectID: r.UserID, SubjectType: string(platformprincipal.TypeUser)}, "forced logout")
		h.fail(c, e)
		return
	}
	OK(c, gin.H{})
}
func (h *UserAuthenticationHandler) record(c *gin.Context, entry securitylog.Entry) error {
	entry.ClientIP = c.ClientIP()
	entry.UserAgent = c.Request.UserAgent()
	return h.security.Record(c.Request.Context(), entry)
}
func (h *UserAuthenticationHandler) recordFailure(c *gin.Context, operationErr error, entry securitylog.Entry, operation string) {
	if errors.Is(operationErr, authentication.ErrSecurityUnavailable) {
		return
	}
	entry.Succeeded = false
	entry.ErrorCode = authenticationFailureCode(operationErr)
	entry.ErrorMessage = operation + " failed"
	if err := h.record(c, entry); err != nil {
		h.logger.ErrorContext(c.Request.Context(), "record failed authentication operation", "operation", operation, "error", err, "request_id", requestID(c))
	}
}
func authenticationFailureCode(err error) string {
	switch {
	case errors.Is(err, authentication.ErrInvalidCredentials), errors.Is(err, authentication.ErrRefreshInvalid):
		return "invalid_credentials"
	case errors.Is(err, authentication.ErrRefreshReused):
		return "refresh_token_reuse"
	case errors.Is(err, authentication.ErrAccountLocked):
		return "account_locked"
	case errors.Is(err, authentication.ErrForbidden):
		return "forbidden"
	case errors.Is(err, authentication.ErrInvalid):
		return "invalid_request"
	case errors.Is(err, authentication.ErrSessionNotFound):
		return "session_not_found"
	default:
		return "internal_error"
	}
}
func loginFailureReason(err error) string {
	if errors.Is(err, authentication.ErrAccountLocked) {
		return "account_locked"
	}
	return "invalid_credentials"
}
func (h *UserAuthenticationHandler) fail(c *gin.Context, e error) {
	switch {
	case errors.Is(e, authentication.ErrInvalidCredentials), errors.Is(e, authentication.ErrRefreshInvalid), errors.Is(e, authentication.ErrRefreshReused), errors.Is(e, authentication.ErrAccountLocked):
		Fail(c, h.logger, apperror.Unauthorized("invalid credentials"))
	case errors.Is(e, authentication.ErrForbidden):
		Fail(c, h.logger, apperror.Forbidden("authentication operation denied"))
	case errors.Is(e, authentication.ErrInvalid):
		Fail(c, h.logger, apperror.Invalid("invalid authentication request", e))
	case errors.Is(e, authentication.ErrSessionNotFound):
		Fail(c, h.logger, apperror.NotFound("session not found"))
	case errors.Is(e, authentication.ErrSecurityUnavailable):
		Fail(c, h.logger, apperror.Unavailable("security audit is unavailable", e))
	case errors.Is(e, identity.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("user not found"))
	default:
		Fail(c, h.logger, apperror.Internal(e))
	}
}
