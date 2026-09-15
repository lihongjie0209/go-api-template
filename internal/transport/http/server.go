package httptransport

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	stdpprof "net/http/pprof"
	"strings"

	"github.com/gin-gonic/gin"
	docs "github.com/lihongjie0209/go-api-template/docs"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/buildinfo"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/health"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/ratelimit"
	"github.com/lihongjie0209/go-api-template/internal/routepolicy"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.uber.org/fx"
)

func NewServer(lc fx.Lifecycle, cfg config.Config, handler *Handler, fileHandler *FileHandler, userHandler *UserHandler, tenantHandler *TenantHandler, tenantMemberHandler *TenantMemberHandler, departmentHandler *DepartmentHandler, tenantAuthorizationHandler *TenantAuthorizationHandler, platformConfigHandler *PlatformConfigHandler, menuHandler *MenuHandler, permissionHandler *PermissionHandler, routePolicyHandler *RoutePolicyHandler, operationLogHandler *OperationLogHandler, authenticationHandler *AuthenticationHandler, userAuthenticationHandler *UserAuthenticationHandler, authService *auth.Service, authorizer platformauthz.Authorizer, routePolicies *routepolicy.Manager, routeRepository *routepolicy.Repository, limiter *ratelimit.Limiter, idempotencyManager *idempotency.Manager, metrics *observability.Metrics, tracing *observability.Tracing, logger *slog.Logger) (*http.Server, error) {
	if cfg.App.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	if err := router.SetTrustedProxies(cfg.HTTP.TrustedProxies); err != nil {
		return nil, fmt.Errorf("configure trusted proxies: %w", err)
	}
	_ = tracing
	router.Use(RequestID(), IdempotencyKey(logger), Environment(cfg.Runtime.ActiveProfile), otelgin.Middleware(cfg.App.Name), RequestLogger(logger), Recovery(logger), HTTPMetrics(metrics), SecurityHeaders(), CORS(cfg.HTTP.CORS), MaxBody(cfg.HTTP.MaxBodyBytes), Timeout(cfg.HTTP.RequestTimeout, logger), RequireJSON())
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		router.Handle(method, "/live", handler.Live)
		router.Handle(method, "/ready", handler.Ready)
	}
	if metrics.Enabled() {
		router.GET("/metrics", gin.WrapH(metrics.Handler()))
	}
	if cfg.Observability.PprofEnabled {
		registerPprof(router.Group("/debug/pprof", pprofAuth(cfg.Observability.PprofToken)))
	}
	if cfg.Swagger.Enabled {
		docs.SwaggerInfo.Version = buildinfo.Version
		swagger := router.Group("/swagger")
		if cfg.Swagger.RequireAuth {
			swagger.Use(JWT(authService, logger))
		}
		swagger.GET("/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	}
	// JWKS clients require the standard GET endpoint and raw RFC 7517 document.
	router.GET("/.well-known/jwks.json", handler.JWKS)
	api := router.Group("/api/v1", RateLimit(limiter, cfg.RateLimit.IP, "ip", func(c *gin.Context) string { return c.ClientIP() }, logger), RateLimit(limiter, cfg.RateLimit.API, "api", func(c *gin.Context) string { return c.FullPath() }, logger), DatabaseAuthentication(authService, logger, cfg.Auth), DatabaseAuthorization(cfg.Authorization.Enabled, cfg.App.Name, authorizer, routePolicies, logger), RateLimit(limiter, cfg.RateLimit.User, "user", func(c *gin.Context) string {
		value, _ := c.Get("subject")
		subject, _ := value.(string)
		return subject
	}, logger))
	api.Use(IdempotencyExecution(idempotencyManager, cfg.Idempotency.HTTPPaths, logger))
	api.POST("/version", handler.Version)
	api.POST("/.well-known/jwks.json", handler.JWKS)
	api.POST("/auth/login", RateLimit(limiter, cfg.RateLimit.Login, "login", func(c *gin.Context) string { return c.ClientIP() }, logger), authenticationHandler.Login)
	api.POST("/auth/user/login", RateLimit(limiter, cfg.RateLimit.Login, "user-login", func(c *gin.Context) string { return c.ClientIP() }, logger), userAuthenticationHandler.Login)
	api.POST("/auth/refresh", userAuthenticationHandler.Refresh)
	api.POST("/auth/logout", userAuthenticationHandler.Logout)
	api.POST("/auth/password/reset", userAuthenticationHandler.SetPassword)
	api.POST("/auth/password/change", userAuthenticationHandler.ChangePassword)
	api.POST("/auth/sessions/page", userAuthenticationHandler.Sessions)
	api.POST("/auth/sessions/revoke", userAuthenticationHandler.RevokeSession)
	api.POST("/auth/sessions/logout-all", userAuthenticationHandler.LogoutAll)
	api.POST("/auth/sessions/force-logout-all", userAuthenticationHandler.ForceLogoutAll)
	api.POST("/me", handler.Me)
	api.POST("/example/ping", handler.Ping)
	api.POST("/files/upload", fileHandler.Upload)
	api.POST("/files/get", fileHandler.Get)
	api.POST("/files/page", fileHandler.Page)
	api.POST("/files/download", fileHandler.Download)
	api.POST("/files/delete", fileHandler.Delete)
	api.POST("/users/create", userHandler.Create)
	api.POST("/users/get", userHandler.Get)
	api.POST("/users/page", userHandler.Page)
	api.POST("/users/update", userHandler.Update)
	api.POST("/users/delete", userHandler.Delete)
	api.POST("/tenants/create", tenantHandler.Create)
	api.POST("/tenants/get", tenantHandler.Get)
	api.POST("/tenants/page", tenantHandler.Page)
	api.POST("/tenants/update", tenantHandler.Update)
	api.POST("/tenants/delete", tenantHandler.Delete)
	api.POST("/tenant-members/add", tenantMemberHandler.Add)
	api.POST("/tenant-members/get", tenantMemberHandler.Get)
	api.POST("/tenant-members/page", tenantMemberHandler.Page)
	api.POST("/tenant-members/status/update", tenantMemberHandler.UpdateStatus)
	api.POST("/tenant-members/remove", tenantMemberHandler.Remove)
	api.POST("/tenant-context/available", tenantMemberHandler.AvailableTenants)
	api.POST("/tenant-context/switch", tenantMemberHandler.SwitchTenant)
	api.POST("/tenant-context/current", tenantMemberHandler.CurrentTenant)
	api.POST("/tenant-departments/create", departmentHandler.Create)
	api.POST("/tenant-departments/get", departmentHandler.Get)
	api.POST("/tenant-departments/tree", departmentHandler.Tree)
	api.POST("/tenant-departments/update", departmentHandler.Update)
	api.POST("/tenant-departments/delete", departmentHandler.Delete)
	api.POST("/tenant-departments/members/set", departmentHandler.SetMembers)
	api.POST("/tenant-authorization/permissions/set", tenantAuthorizationHandler.SetTenantPermissions)
	api.POST("/tenant-authorization/administrators/set", tenantAuthorizationHandler.SetAdministrator)
	api.POST("/tenant-authorization/effective-permissions", tenantAuthorizationHandler.EffectivePermissions)
	api.POST("/tenant-roles/create", tenantAuthorizationHandler.CreateRole)
	api.POST("/tenant-roles/get", tenantAuthorizationHandler.GetRole)
	api.POST("/tenant-roles/page", tenantAuthorizationHandler.PageRoles)
	api.POST("/tenant-roles/update", tenantAuthorizationHandler.UpdateRole)
	api.POST("/tenant-roles/delete", tenantAuthorizationHandler.DeleteRole)
	api.POST("/tenant-roles/permissions/get", tenantAuthorizationHandler.RolePermissions)
	api.POST("/tenant-roles/permissions/set", tenantAuthorizationHandler.SetRolePermissions)
	api.POST("/tenant-members/roles/set", tenantAuthorizationHandler.SetMemberRoles)
	api.POST("/tenant-members/roles/get", tenantAuthorizationHandler.MemberRoles)
	api.POST("/platform-configs/create", platformConfigHandler.Create)
	api.POST("/platform-configs/get", platformConfigHandler.Get)
	api.POST("/platform-configs/page", platformConfigHandler.Page)
	api.POST("/platform-configs/update", platformConfigHandler.Update)
	api.POST("/platform-configs/delete", platformConfigHandler.Delete)
	api.POST("/public/platform-configs/get", platformConfigHandler.GetPublic)
	api.POST("/public/platform-configs/list", platformConfigHandler.ListPublic)
	api.POST("/menus/create", menuHandler.Create)
	api.POST("/menus/get", menuHandler.Get)
	api.POST("/menus/tree", menuHandler.Tree)
	api.POST("/menus/update", menuHandler.Update)
	api.POST("/menus/delete", menuHandler.Delete)
	api.POST("/me/menus", menuHandler.Current)
	api.POST("/permissions/tree", permissionHandler.Tree)
	api.POST("/permissions/create", permissionHandler.Create)
	api.POST("/permissions/get", permissionHandler.Get)
	api.POST("/permissions/update", permissionHandler.Update)
	api.POST("/permissions/delete", permissionHandler.Delete)
	api.POST("/route-policies/get", routePolicyHandler.Get)
	api.POST("/route-policies/page", routePolicyHandler.Page)
	api.POST("/route-policies/set", routePolicyHandler.Set)
	api.POST("/operation-logs/frontend/record", operationLogHandler.RecordFrontend)
	api.POST("/operation-logs/get", operationLogHandler.Get)
	api.POST("/operation-logs/page", operationLogHandler.Page)
	server := &http.Server{Addr: cfg.HTTP.Address, Handler: router, ReadTimeout: cfg.HTTP.ReadTimeout, WriteTimeout: cfg.HTTP.WriteTimeout, IdleTimeout: cfg.HTTP.IdleTimeout}
	var listener net.Listener
	policyContext, stopPolicies := context.WithCancel(context.Background())
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		if cfg.Authorization.Enabled {
			routes, err := discoveredBusinessRoutes(router, cfg.App.Name)
			if err != nil {
				return err
			}
			if err := routeRepository.SyncRoutes(ctx, routes, cfg.App.Name+":route-discovery"); err != nil {
				return fmt.Errorf("sync route definitions: %w", err)
			}
			if err := routePolicies.Refresh(ctx); err != nil {
				return fmt.Errorf("load route policies: %w", err)
			}
			if err := routePolicies.ValidateRoutes(ctx, cfg.App.Name); err != nil {
				logger.Warn("one or more HTTP routes have no active database policy; affected requests will be denied", "error", err)
			}
			go routePolicies.Run(policyContext)
		}
		var err error
		listener, err = net.Listen("tcp", server.Addr)
		if err != nil {
			return fmt.Errorf("listen http: %w", err)
		}
		go func() {
			if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				logger.Error("http server stopped unexpectedly", "error", serveErr)
			}
		}()
		logger.Info("http server started", "address", server.Addr)
		return nil
	}, OnStop: func(ctx context.Context) error {
		stopPolicies()
		return server.Shutdown(ctx)
	}})
	return server, nil
}

func pprofAuth(expected string) gin.HandlerFunc {
	return func(c *gin.Context) {
		scheme, token, ok := strings.Cut(c.GetHeader("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	}
}

func registerPprof(group *gin.RouterGroup) {
	group.GET("/", gin.WrapF(stdpprof.Index))
	group.GET("/cmdline", gin.WrapF(stdpprof.Cmdline))
	group.GET("/profile", gin.WrapF(stdpprof.Profile))
	group.POST("/symbol", gin.WrapF(stdpprof.Symbol))
	group.GET("/symbol", gin.WrapF(stdpprof.Symbol))
	group.GET("/trace", gin.WrapF(stdpprof.Trace))
	for _, profile := range []string{"allocs", "block", "goroutine", "heap", "mutex", "threadcreate"} {
		group.GET("/"+profile, gin.WrapH(stdpprof.Handler(profile)))
	}
}

func discoveredBusinessRoutes(router *gin.Engine, serviceName string) ([]routepolicy.Route, error) {
	routes := []routepolicy.Route{}
	for _, info := range router.Routes() {
		if !strings.HasPrefix(info.Path, "/api/v1/") {
			continue
		}
		route, err := routepolicy.NewRoute("http", info.Method, info.Path, serviceName, buildinfo.Version)
		if err != nil {
			return nil, fmt.Errorf("describe route %s %s: %w", info.Method, info.Path, err)
		}
		route.Operation = info.Handler
		routes = append(routes, route)
	}
	return routes, nil
}

var Module = fx.Module("http", fx.Provide(auth.NewRuntime, health.New, ratelimit.New, NewHandler, NewFileHandler, NewUserHandler, NewTenantHandler, NewTenantMemberHandler, NewDepartmentHandler, NewTenantAuthorizationHandler, NewPlatformConfigHandler, NewMenuHandler, NewPermissionHandler, NewRoutePolicyHandler, NewOperationLogHandler, NewAuthenticationHandler, NewUserAuthenticationHandler, NewServer), fx.Invoke(func(*http.Server) {}))
