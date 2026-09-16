package apperror

import (
	"net/http"

	platformcode "github.com/lihongjie0209/microservice-platform-go/errorcode"
)

type Error struct {
	Code       int    `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"-"`
	Err        error  `json:"-"`
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Message
}
func (e *Error) Unwrap() error { return e.Err }

const (
	CodeOK                       = int(platformcode.OK)
	CodeInvalidArgument          = int(platformcode.InvalidArgument)
	CodeNotFound                 = int(platformcode.NotFound)
	CodeRequestTimeout           = int(platformcode.RequestTimeout)
	CodeTooManyRequests          = int(platformcode.TooManyRequests)
	CodeUnauthorized             = int(platformcode.Unauthorized)
	CodeForbidden                = int(platformcode.Forbidden)
	CodeConflict                 = int(platformcode.Conflict)
	CodeRequestInProgress        = int(platformcode.RequestInProgress)
	CodeInternal                 = int(platformcode.Internal)
	CodeDependencyUnavailable    = int(platformcode.DependencyUnavailable)
	CodeAuthorizationUnavailable = int(platformcode.AuthorizationUnavailable)
	CodePermissionPolicyMissing  = int(platformcode.PermissionPolicyMissing)
)

func New(code int, message string, status int, err error) *Error {
	return &Error{Code: code, Message: message, HTTPStatus: status, Err: err}
}
func Invalid(message string, err error) *Error {
	return New(CodeInvalidArgument, message, http.StatusBadRequest, err)
}
func NotFound(message string) *Error {
	return New(CodeNotFound, message, http.StatusNotFound, nil)
}
func MethodNotAllowed() *Error {
	return New(CodeInvalidArgument, "method not allowed", http.StatusMethodNotAllowed, nil)
}
func Conflict(message string, err error) *Error {
	return New(CodeConflict, message, http.StatusConflict, err)
}
func RequestInProgress() *Error {
	return New(CodeRequestInProgress, "request is already processing", http.StatusConflict, nil)
}
func Unauthorized(message string) *Error {
	return New(CodeUnauthorized, message, http.StatusUnauthorized, nil)
}
func Forbidden(message string) *Error {
	return New(CodeForbidden, message, http.StatusForbidden, nil)
}
func TooManyRequests() *Error {
	return New(CodeTooManyRequests, "too many requests", http.StatusTooManyRequests, nil)
}
func RequestTimeout() *Error {
	return New(CodeRequestTimeout, "request timeout", http.StatusGatewayTimeout, nil)
}
func Unavailable(message string, err error) *Error {
	return New(CodeDependencyUnavailable, message, http.StatusServiceUnavailable, err)
}
func Internal(err error) *Error {
	return New(CodeInternal, "internal server error", http.StatusInternalServerError, err)
}
func AuthorizationUnavailable(err error) *Error {
	return New(CodeAuthorizationUnavailable, "authorization decision is unavailable", http.StatusServiceUnavailable, err)
}
func PermissionPolicyMissing(err error) *Error {
	return New(CodePermissionPolicyMissing, "authorization policy is not configured", http.StatusInternalServerError, err)
}
