package httptransport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/environment"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	appLimit "github.com/lihongjie0209/go-api-template/internal/ratelimit"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"go.opentelemetry.io/otel/trace"
)

const requestIDKey = "request_id"

func SecurityClientContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := securitylog.WithClient(c.Request.Context(), c.ClientIP(), c.Request.UserAgent())
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if !requestid.Valid(id) {
			id = requestid.Generate()
		}
		c.Set(requestIDKey, id)
		c.Header("X-Request-ID", id)
		c.Request = c.Request.WithContext(requestid.WithContext(c.Request.Context(), id))
		c.Next()
	}
}

func Environment(profile string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("environment", profile)
		c.Request = c.Request.WithContext(environment.WithContext(c.Request.Context(), profile))
		c.Next()
	}
}

func IdempotencyKey(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetHeader("Idempotency-Key")
		if key == "" {
			c.Next()
			return
		}
		if !idempotency.Valid(key) {
			Fail(c, logger, apperror.Invalid("invalid Idempotency-Key", nil))
			return
		}
		c.Header("Idempotency-Key", key)
		c.Request = c.Request.WithContext(idempotency.WithContext(c.Request.Context(), key))
		c.Next()
	}
}

type idempotencyManager interface {
	Enabled() bool
	MaxResponseBytes() int
	Begin(context.Context, string, string) (idempotency.Decision, error)
	Complete(context.Context, string, string, any) error
	Fail(context.Context, string, string, idempotency.Failure) error
	Abort(context.Context, string, string) error
	StartLease(context.Context, string, string) (context.Context, func() error, error)
}

type responseCapture struct {
	gin.ResponseWriter
	body     bytes.Buffer
	maxBytes int
	overflow bool
}

func (w *responseCapture) Write(value []byte) (int, error) {
	w.capture(value)
	return w.ResponseWriter.Write(value)
}

func (w *responseCapture) WriteString(value string) (int, error) {
	w.capture([]byte(value))
	return w.ResponseWriter.WriteString(value)
}

func (w *responseCapture) capture(value []byte) {
	remaining := w.maxBytes - w.body.Len()
	if remaining <= 0 {
		w.overflow = w.overflow || len(value) > 0
		return
	}
	if len(value) > remaining {
		_, _ = w.body.Write(value[:remaining])
		w.overflow = true
		return
	}
	_, _ = w.body.Write(value)
}

func IdempotencyExecution(manager idempotencyManager, paths []string, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		key, ok := idempotency.FromContext(c.Request.Context())
		if !ok || manager == nil || !manager.Enabled() || c.Request.Method != http.MethodPost || !auth.MatchesAny(c.FullPath(), paths) {
			c.Next()
			return
		}
		fingerprint, err := idempotencyFingerprint(c)
		if err != nil {
			Fail(c, logger, apperror.Invalid("read idempotent request", err))
			return
		}
		decision, err := manager.Begin(c.Request.Context(), key, fingerprint)
		if err != nil {
			Fail(c, logger, apperror.Unavailable("idempotency is unavailable", err))
			return
		}
		switch decision.State {
		case idempotency.StateCompleted:
			var response Response
			if err := json.Unmarshal(decision.Response, &response); err != nil {
				Fail(c, logger, apperror.Unavailable("idempotency response is unavailable", err))
				return
			}
			response.RequestID = requestID(c)
			c.Abort()
			c.JSON(http.StatusOK, response)
			return
		case idempotency.StateFailed:
			c.AbortWithStatusJSON(decision.Failure.HTTPStatus, Response{Code: decision.Failure.Code, Message: decision.Failure.Message, Body: nil, RequestID: requestID(c)})
			return
		case idempotency.StateProcessing:
			Fail(c, logger, apperror.RequestInProgress())
			return
		case idempotency.StateConflict:
			Fail(c, logger, apperror.Conflict("idempotency key belongs to a different request", nil))
			return
		case idempotency.StateAcquired:
		default:
			Fail(c, logger, apperror.Unavailable("idempotency state is invalid", nil))
			return
		}
		leaseCtx, stopLease, err := manager.StartLease(c.Request.Context(), key, decision.Owner)
		if err != nil {
			Fail(c, logger, apperror.Unavailable("idempotency lease is unavailable", err))
			return
		}
		c.Request = c.Request.WithContext(leaseCtx)

		capture := &responseCapture{ResponseWriter: c.Writer, maxBytes: manager.MaxResponseBytes()}
		c.Writer = capture
		c.Next()
		if leaseErr := stopLease(); leaseErr != nil {
			logger.ErrorContext(c.Request.Context(), "idempotency lease lost", "error", leaseErr, "request_id", requestID(c))
			return
		}
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 2*time.Second)
		defer cancel()
		if capture.overflow {
			if err := manager.Abort(persistCtx, key, decision.Owner); err != nil {
				logger.ErrorContext(c.Request.Context(), "release oversized idempotency response", "error", err, "request_id", requestID(c))
			}
			return
		}
		var response Response
		if err := json.Unmarshal(capture.body.Bytes(), &response); err != nil {
			logger.ErrorContext(c.Request.Context(), "idempotency response was not unified JSON", "error", err, "request_id", requestID(c))
			return
		}
		response.RequestID = ""
		if retryableHTTPStatus(c.Writer.Status()) {
			err = manager.Abort(persistCtx, key, decision.Owner)
		} else if c.Writer.Status() >= http.StatusBadRequest {
			err = manager.Fail(persistCtx, key, decision.Owner, idempotency.Failure{Code: response.Code, Message: response.Message, HTTPStatus: c.Writer.Status()})
		} else {
			err = manager.Complete(persistCtx, key, decision.Owner, response)
		}
		if err != nil {
			logger.ErrorContext(c.Request.Context(), "persist idempotency result", "error", err, "request_id", requestID(c))
		}
	}
}

func retryableHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooEarly || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func idempotencyFingerprint(c *gin.Context) (string, error) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return "", err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	canonicalBody, err := canonicalJSON(body)
	if err != nil {
		return "", err
	}
	caller, _ := platformprincipal.FromContext(c.Request.Context())
	hash := sha256.New()
	_, _ = io.WriteString(hash, caller.ID)
	_, _ = io.WriteString(hash, "\x00"+string(caller.Type))
	_, _ = io.WriteString(hash, "\x00"+caller.TenantID)
	_, _ = io.WriteString(hash, "\x00"+caller.MembershipID)
	_, _ = io.WriteString(hash, "\x00"+caller.SessionID)
	_, _ = io.WriteString(hash, "\x00"+c.Request.Method+"\x00"+c.FullPath()+"\x00")
	_, _ = hash.Write(canonicalBody)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func canonicalJSON(body []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("request contains multiple JSON values")
		}
		return nil, err
	}
	return json.Marshal(value)
}

func RequestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		span := trace.SpanFromContext(c.Request.Context()).SpanContext()
		logger.InfoContext(c.Request.Context(), "http request", "request_id", requestID(c), "trace_id", span.TraceID().String(), "span_id", span.SpanID().String(), "method", c.Request.Method, "path", c.FullPath(), "status", c.Writer.Status(), "duration", time.Since(started), "client_ip", c.ClientIP())
	}
}

func HTTPMetrics(metrics *observability.Metrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		if !metrics.Enabled() {
			return
		}
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		metrics.HTTPRequests.WithLabelValues(c.Request.Method, route, strconv.Itoa(c.Writer.Status())).Inc()
		metrics.HTTPDuration.WithLabelValues(c.Request.Method, route).Observe(time.Since(started).Seconds())
	}
}

func Recovery(logger *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered any) {
		logger.ErrorContext(c.Request.Context(), "http panic recovered", "request_id", requestID(c), "panic", recovered)
		c.AbortWithStatusJSON(http.StatusInternalServerError, Response{Code: apperror.CodeInternal, Message: "internal server error", Body: nil, RequestID: requestID(c)})
	})
}

func Timeout(timeout time.Duration, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
		if ctx.Err() == context.DeadlineExceeded && !c.Writer.Written() {
			Fail(c, logger, apperror.RequestTimeout())
		}
	}
}

func MaxBody(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) { c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit); c.Next() }
}

func RequireJSON() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodPost && c.Request.ContentLength != 0 {
			mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
			allowed := mediaType == "application/json" || (c.Request.URL.Path == "/api/v1/files/upload" && mediaType == "multipart/form-data")
			if err != nil || !allowed {
				c.AbortWithStatusJSON(http.StatusUnsupportedMediaType, Response{Code: apperror.CodeInvalidArgument, Message: "unsupported content type", Body: nil, RequestID: requestID(c)})
				return
			}
		}
		c.Next()
	}
}

func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		c.Header("Cross-Origin-Resource-Policy", "same-origin")
		c.Header("Cache-Control", "no-store")
		// Browsers ignore HSTS received over plaintext HTTP. Always emitting it
		// also covers TLS terminated by a trusted reverse proxy.
		c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		c.Next()
	}
}

func SwaggerSecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		c.Next()
	}
}

func CORS(cfg config.CORS) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(cfg.AllowedOrigins))
	for _, origin := range cfg.AllowedOrigins {
		allowed[origin] = struct{}{}
	}
	return func(c *gin.Context) {
		if !cfg.Enabled {
			c.Next()
			return
		}
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		if _, ok := allowed[origin]; !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, Response{Code: apperror.CodeForbidden, Message: "origin is not allowed", Body: nil, RequestID: requestID(c)})
			return
		}
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Vary", "Origin")
		c.Header("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		c.Header("Access-Control-Allow-Headers", strings.Join(cfg.AllowedHeaders, ", "))
		c.Header("Access-Control-Expose-Headers", strings.Join(cfg.ExposedHeaders, ", "))
		c.Header("Access-Control-Max-Age", strconv.Itoa(int(cfg.MaxAge.Seconds())))
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

type RateKeyFunc func(*gin.Context) string

func RateLimit(limiter *appLimit.Limiter, rule config.RateLimitRule, dimension string, keyFunc RateKeyFunc, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !limiter.Enabled() {
			c.Next()
			return
		}
		key := keyFunc(c)
		if key == "" {
			c.Next()
			return
		}
		result, err := limiter.Allow(c.Request.Context(), "rate:"+dimension+":"+key, rule)
		if err != nil {
			// Login throttling is an account-protection boundary and must never
			// inherit the availability-oriented fail-open policy.
			if limiter.FailOpen() && dimension != "login" {
				logger.WarnContext(c.Request.Context(), "rate limit check failed open", "request_id", requestID(c), "dimension", dimension, "error", err)
				c.Next()
				return
			}
			Fail(c, logger, apperror.Unavailable("rate limiter unavailable", err))
			return
		}
		c.Header("X-RateLimit-Limit", strconv.Itoa(result.Limit))
		c.Header("X-RateLimit-Remaining", strconv.Itoa(result.Remaining))
		if !result.Allowed {
			retry := max(1, int(result.RetryAfter.Round(time.Second)/time.Second))
			c.Header("Retry-After", strconv.Itoa(retry))
			Fail(c, logger, apperror.TooManyRequests())
			return
		}
		c.Next()
	}
}

func JWT(service *auth.Service, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		scheme, raw, ok := strings.Cut(header, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || raw == "" {
			Fail(c, logger, apperror.Unauthorized("missing bearer token"))
			return
		}
		identity, err := service.Verify(c.Request.Context(), raw)
		if err != nil {
			Fail(c, logger, apperror.Unauthorized("invalid or expired token"))
			return
		}
		c.Set("subject", identity.ID)
		ctx := platformprincipal.WithContext(c.Request.Context(), identity)
		c.Request = c.Request.WithContext(platformauthz.WithCallerCredential(ctx, header))
		c.Next()
	}
}

// DatabaseAuthentication verifies credentials when supplied. The immutable
// endpoint descriptor decides whether an anonymous principal may proceed, and
// protected operations are subsequently evaluated by the PBAC engine.
func DatabaseAuthentication(service *auth.Service, logger *slog.Logger, cfg config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := strings.TrimSpace(c.GetHeader("Authorization"))
		if header == "" {
			c.Next()
			return
		}
		scheme, raw, ok := strings.Cut(header, " ")
		if !ok || raw == "" {
			Fail(c, logger, apperror.Unauthorized("invalid authorization credential"))
			return
		}
		var identity platformprincipal.Principal
		switch {
		case strings.EqualFold(scheme, "Bearer"):
			verified, state, err := service.VerifyWithState(c.Request.Context(), raw)
			if err != nil {
				Fail(c, logger, apperror.Unauthorized("invalid or expired token"))
				return
			}
			identity = verified
			c.Set(passwordChangeRequiredKey, state.MustChangePassword)
			c.Request = c.Request.WithContext(accesscontrol.WithCredentialScheme(c.Request.Context(), accesscontrol.CredentialSchemeBearer))
		case strings.EqualFold(scheme, "PSK"):
			if !cfg.Auth.PSK.Enabled || !auth.VerifyPSK(header, cfg.Auth.PSK.Key) {
				Fail(c, logger, apperror.Unauthorized("invalid PSK"))
				return
			}
			identity = platformprincipal.Principal{ID: cfg.App.Name + ":psk", Type: platformprincipal.TypeServiceAccount}
			c.Request = c.Request.WithContext(accesscontrol.WithCredentialScheme(c.Request.Context(), accesscontrol.CredentialSchemePSK))
		default:
			Fail(c, logger, apperror.Unauthorized("unsupported authorization scheme"))
			return
		}
		c.Set("subject", identity.ID)
		ctx := platformprincipal.WithContext(c.Request.Context(), identity)
		c.Request = c.Request.WithContext(platformauthz.WithCallerCredential(ctx, header))
		c.Next()
	}
}

const passwordChangeRequiredKey = "password_change_required"

func PasswordChangeGate(logger *slog.Logger) gin.HandlerFunc {
	allowed := map[string]struct{}{
		"/api/v1/auth/logout":          {},
		"/api/v1/auth/password/change": {},
		"/api/v1/auth/refresh":         {},
	}
	return func(c *gin.Context) {
		required, _ := c.Get(passwordChangeRequiredKey)
		if mustChange, _ := required.(bool); !mustChange {
			c.Next()
			return
		}
		if _, ok := allowed[c.FullPath()]; ok {
			c.Next()
			return
		}
		Fail(c, logger, apperror.Forbidden("password change required"))
	}
}

func requestID(c *gin.Context) string {
	value, _ := c.Get(requestIDKey)
	id, _ := value.(string)
	return id
}
