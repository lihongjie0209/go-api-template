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
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/buildinfo"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/health"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/lihongjie0209/go-api-template/internal/ratelimit"
	"github.com/lihongjie0209/go-api-template/internal/serviceaccount"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.uber.org/fx"
)

func NewServer(lc fx.Lifecycle, cfg config.Config, handler *Handler, fileHandler *FileHandler, userHandler *UserHandler, serviceAccountHandler *ServiceAccountHandler, tenantHandler *TenantHandler, tenantMemberHandler *TenantMemberHandler, departmentHandler *DepartmentHandler, tenantAuthorizationHandler *TenantAuthorizationHandler, capabilityHandler *CapabilityHandler, capabilityService *authorization.CapabilityService, applicationHandler *ApplicationHandler, navigationHandler *NavigationHandler, platformConfigHandler *PlatformConfigHandler, dictionaryHandler *DictionaryHandler, pbacHandler *PBACHandler, dataPermissionHandler *DataPermissionHandler, permissionHandler *PermissionHandler, operationLogHandler *OperationLogHandler, securityLogHandler *SecurityLogHandler, authenticationHandler *AuthenticationHandler, userAuthenticationHandler *UserAuthenticationHandler, authService *auth.Service, resources *pbac.Registry, schemas *datapermission.SchemaRegistry, authorizer platformauthz.Authorizer, limiter *ratelimit.Limiter, idempotencyManager *idempotency.Manager, metrics *observability.Metrics, tracing *observability.Tracing, logger *slog.Logger) (*http.Server, error) {
	if cfg.App.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	// Multipart parsing may retain form data in memory before spilling to disk.
	// Keep that independent ceiling small even when the request limit permits
	// multi-gigabyte object uploads.
	router.MaxMultipartMemory = 8 << 20
	if err := router.SetTrustedProxies(cfg.HTTP.TrustedProxies); err != nil {
		return nil, fmt.Errorf("configure trusted proxies: %w", err)
	}
	_ = tracing
	router.Use(RequestID(), IdempotencyKey(logger), Environment(cfg.Runtime.ActiveProfile), otelgin.Middleware(cfg.App.Name), RequestLogger(logger), Recovery(logger), HTTPMetrics(metrics), SecurityHeaders(), CORS(cfg.HTTP.CORS), MaxBody(cfg.HTTP.MaxBodyBytes), Timeout(cfg.HTTP.RequestTimeout, logger), RequireJSON(), SecurityClientContext())
	configureRouterContract(router, logger)
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
		swagger.Use(SwaggerSecurityHeaders())
		if cfg.Swagger.RequireAuth {
			swagger.Use(JWT(authService, logger))
		}
		swagger.GET("/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	}
	// JWKS clients require the standard GET endpoint and raw RFC 7517 document.
	router.GET("/.well-known/jwks.json", handler.JWKS)
	api := router.Group("/api/v1", RateLimit(limiter, cfg.RateLimit.IP, "ip", func(c *gin.Context) string { return c.ClientIP() }, logger), RateLimit(limiter, cfg.RateLimit.API, "api", func(c *gin.Context) string { return c.FullPath() }, logger), DatabaseAuthentication(authService, logger, cfg), RateLimit(limiter, cfg.RateLimit.User, "user", func(c *gin.Context) string {
		value, _ := c.Get("subject")
		subject, _ := value.(string)
		return subject
	}, logger))
	api.Use(IdempotencyExecution(idempotencyManager, cfg.Idempotency.HTTPPaths, logger))
	var registrationErr error
	definitions := make([]accesscontrol.Endpoint, 0, 128)
	post := func(endpoint accesscontrol.Endpoint, handlers ...gin.HandlerFunc) {
		if registrationErr == nil {
			registrationErr = registerPOST(api, resources, schemas, authorizer, logger, endpoint, handlers...)
			if registrationErr == nil {
				definitions = append(definitions, endpoint)
			}
		}
	}
	public := func(path string, handlers ...gin.HandlerFunc) { post(publicEndpoint("/api/v1"+path), handlers...) }
	jwt := func(path, resource, action string, data accesscontrol.DataPermissionMode, handlers ...gin.HandlerFunc) {
		post(jwtEndpoint("/api/v1"+path, resource, action, data), handlers...)
	}
	public("/version", handler.Version)
	public("/.well-known/jwks.json", handler.JWKSAPI)
	public("/auth/login", RateLimit(limiter, cfg.RateLimit.Login, "login", func(c *gin.Context) string { return c.ClientIP() }, logger), authenticationHandler.Login)
	public("/auth/user/login", RateLimit(limiter, cfg.RateLimit.Login, "user-login", func(c *gin.Context) string { return c.ClientIP() }, logger), userAuthenticationHandler.Login)
	public("/auth/refresh", userAuthenticationHandler.Refresh)
	jwt("/auth/logout", "identity.session", "logout", accesscontrol.DataPermissionNone, userAuthenticationHandler.Logout)
	jwt("/auth/password/reset", "identity.user", "reset-password", accesscontrol.DataPermissionNone, userAuthenticationHandler.SetPassword)
	jwt("/auth/password/change", "identity.credential", "change-password", accesscontrol.DataPermissionNone, userAuthenticationHandler.ChangePassword)
	jwt("/auth/sessions/page", "identity.session", "list", accesscontrol.DataPermissionNone, userAuthenticationHandler.Sessions)
	jwt("/auth/sessions/revoke", "identity.session", "revoke", accesscontrol.DataPermissionNone, userAuthenticationHandler.RevokeSession)
	jwt("/auth/sessions/logout-all", "identity.session", "logout", accesscontrol.DataPermissionNone, userAuthenticationHandler.LogoutAll)
	jwt("/auth/sessions/force-logout-all", "identity.user", "force-logout", accesscontrol.DataPermissionNone, userAuthenticationHandler.ForceLogoutAll)
	jwt("/me", "identity.profile", "read", accesscontrol.DataPermissionNone, handler.Me)
	jwt("/authorization/capabilities/evaluate", "authorization.capability", "evaluate", accesscontrol.DataPermissionNone, capabilityHandler.Evaluate)
	jwt("/authorization/rows/evaluate", "authorization.capability", "evaluate", accesscontrol.DataPermissionNone, capabilityHandler.EvaluateRows)
	public("/example/ping", handler.Ping)
	jwt("/files/upload", "file.object", "create", accesscontrol.DataPermissionNone, fileHandler.Upload)
	jwt("/files/get", "file.object", "read", accesscontrol.DataPermissionNone, fileHandler.Get)
	jwt("/files/page", "file.object", "list", accesscontrol.DataPermissionNone, fileHandler.Page)
	jwt("/files/download", "file.object", "download", accesscontrol.DataPermissionNone, fileHandler.Download)
	jwt("/files/delete", "file.object", "delete", accesscontrol.DataPermissionNone, fileHandler.Delete)
	jwt("/users/create", "identity.user", "create", accesscontrol.DataPermissionNone, userHandler.Create)
	jwt("/users/get", "identity.user", "read", accesscontrol.DataPermissionNone, userHandler.Get)
	jwt("/users/page", "identity.user", "list", accesscontrol.DataPermissionNone, userHandler.Page)
	jwt("/users/update", "identity.user", "update", accesscontrol.DataPermissionNone, userHandler.Update)
	jwt("/users/delete", "identity.user", "delete", accesscontrol.DataPermissionNone, userHandler.Delete)
	jwt("/service-accounts/create", "identity.service-account", "create", accesscontrol.DataPermissionNone, serviceAccountHandler.Create)
	jwt("/service-accounts/get", "identity.service-account", "read", accesscontrol.DataPermissionNone, serviceAccountHandler.Get)
	jwt("/service-accounts/page", "identity.service-account", "list", accesscontrol.DataPermissionNone, serviceAccountHandler.Page)
	jwt("/service-accounts/update", "identity.service-account", "update", accesscontrol.DataPermissionNone, serviceAccountHandler.Update)
	jwt("/service-accounts/secret/rotate", "identity.service-account", "rotate-secret", accesscontrol.DataPermissionNone, serviceAccountHandler.RotateSecret)
	jwt("/service-accounts/delete", "identity.service-account", "delete", accesscontrol.DataPermissionNone, serviceAccountHandler.Delete)
	jwt("/tenants/get", "tenant.profile", "read", accesscontrol.DataPermissionNone, tenantHandler.Get)
	jwt("/tenants/page", "tenant.profile", "list", accesscontrol.DataPermissionNone, tenantHandler.Page)
	jwt("/tenants/update", "tenant.profile", "update", accesscontrol.DataPermissionNone, tenantHandler.Update)
	jwt("/tenants/delete", "tenant.profile", "delete", accesscontrol.DataPermissionNone, tenantHandler.Delete)
	jwt("/platform/tenants/create", "tenant", "create", accesscontrol.DataPermissionNone, tenantHandler.Create)
	jwt("/platform/tenants/get", "tenant", "read", accesscontrol.DataPermissionNone, tenantHandler.AdminGet)
	jwt("/platform/tenants/page", "tenant", "list", accesscontrol.DataPermissionNone, tenantHandler.AdminPage)
	jwt("/platform/tenants/update", "tenant", "update", accesscontrol.DataPermissionNone, tenantHandler.AdminUpdate)
	jwt("/platform/tenants/delete", "tenant", "delete", accesscontrol.DataPermissionNone, tenantHandler.AdminDelete)
	jwt("/tenant-members/add", "tenant.member", "add", accesscontrol.DataPermissionObject, tenantMemberHandler.Add)
	jwt("/tenant-members/get", "tenant.member", "read", accesscontrol.DataPermissionRequired, tenantMemberHandler.Get)
	jwt("/tenant-members/page", "tenant.member", "list", accesscontrol.DataPermissionRequired, tenantMemberHandler.Page)
	jwt("/tenant-members/status/update", "tenant.member", "update", accesscontrol.DataPermissionRequired, tenantMemberHandler.UpdateStatus)
	jwt("/tenant-members/remove", "tenant.member", "remove", accesscontrol.DataPermissionRequired, tenantMemberHandler.Remove)
	jwt("/tenant-context/available", "tenant.selection", "list", accesscontrol.DataPermissionNone, tenantMemberHandler.AvailableTenants)
	jwt("/tenant-context/switch", "tenant.selection", "switch", accesscontrol.DataPermissionNone, tenantMemberHandler.SwitchTenant)
	jwt("/tenant-context/current", "tenant.context", "read", accesscontrol.DataPermissionNone, tenantMemberHandler.CurrentTenant)
	jwt("/tenant-departments/create", "tenant.department", "create", accesscontrol.DataPermissionObject, departmentHandler.Create)
	jwt("/tenant-departments/get", "tenant.department", "read", accesscontrol.DataPermissionRequired, departmentHandler.Get)
	jwt("/tenant-departments/tree", "tenant.department", "list", accesscontrol.DataPermissionRequired, departmentHandler.Tree)
	jwt("/tenant-departments/update", "tenant.department", "update", accesscontrol.DataPermissionRequired, departmentHandler.Update)
	jwt("/tenant-departments/delete", "tenant.department", "delete", accesscontrol.DataPermissionRequired, departmentHandler.Delete)
	jwt("/tenant-departments/members/set", "tenant.department", "assign-member", accesscontrol.DataPermissionRequired, departmentHandler.SetMembers)
	jwt("/platform/tenant-authorization/permissions/set", "tenant", "grant", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.SetTenantPermissions)
	jwt("/platform/tenant-authorization/administrators/set", "tenant", "assign-administrator", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.SetAdministrator)
	jwt("/tenant-authorization/administrators/set", "tenant.authorization", "assign-administrator", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.SetAdministrator)
	jwt("/tenant-authorization/effective-permissions", "tenant.authorization", "read", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.EffectivePermissions)
	jwt("/tenant-roles/create", "tenant.role", "create", accesscontrol.DataPermissionObject, tenantAuthorizationHandler.CreateRole)
	jwt("/tenant-roles/get", "tenant.role", "read", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.GetRole)
	jwt("/tenant-roles/page", "tenant.role", "list", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.PageRoles)
	jwt("/tenant-roles/update", "tenant.role", "update", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.UpdateRole)
	jwt("/tenant-roles/delete", "tenant.role", "delete", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.DeleteRole)
	jwt("/tenant-roles/permissions/get", "tenant.role", "read", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.RolePermissions)
	jwt("/tenant-roles/permissions/set", "tenant.role", "grant", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.SetRolePermissions)
	jwt("/tenant-members/roles/set", "tenant.member", "assign-role", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.SetMemberRoles)
	jwt("/tenant-members/roles/get", "tenant.member", "read", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.MemberRoles)
	jwt("/platform-configs/create", "platform.config", "create", accesscontrol.DataPermissionNone, platformConfigHandler.Create)
	jwt("/platform-configs/get", "platform.config", "read", accesscontrol.DataPermissionNone, platformConfigHandler.Get)
	jwt("/platform-configs/page", "platform.config", "list", accesscontrol.DataPermissionNone, platformConfigHandler.Page)
	jwt("/platform-configs/update", "platform.config", "update", accesscontrol.DataPermissionNone, platformConfigHandler.Update)
	jwt("/platform-configs/delete", "platform.config", "delete", accesscontrol.DataPermissionNone, platformConfigHandler.Delete)
	public("/public/platform-configs/get", platformConfigHandler.GetPublic)
	public("/public/platform-configs/list", platformConfigHandler.ListPublic)
	public("/public/dictionaries/query", dictionaryHandler.Query)
	jwt("/dictionaries/create", "dictionary.definition", "create", accesscontrol.DataPermissionNone, dictionaryHandler.CreateDefinition)
	jwt("/dictionaries/get", "dictionary.definition", "read", accesscontrol.DataPermissionNone, dictionaryHandler.GetDefinition)
	jwt("/dictionaries/page", "dictionary.definition", "list", accesscontrol.DataPermissionNone, dictionaryHandler.PageDefinitions)
	jwt("/dictionaries/update", "dictionary.definition", "update", accesscontrol.DataPermissionNone, dictionaryHandler.UpdateDefinition)
	jwt("/dictionaries/delete", "dictionary.definition", "delete", accesscontrol.DataPermissionNone, dictionaryHandler.DeleteDefinition)
	jwt("/dictionary-items/create", "dictionary.item", "create", accesscontrol.DataPermissionNone, dictionaryHandler.CreateItem)
	jwt("/dictionary-items/get", "dictionary.item", "read", accesscontrol.DataPermissionNone, dictionaryHandler.GetItem)
	jwt("/dictionary-items/page", "dictionary.item", "list", accesscontrol.DataPermissionNone, dictionaryHandler.PageItems)
	jwt("/dictionary-items/update", "dictionary.item", "update", accesscontrol.DataPermissionNone, dictionaryHandler.UpdateItem)
	jwt("/dictionary-items/delete", "dictionary.item", "delete", accesscontrol.DataPermissionNone, dictionaryHandler.DeleteItem)
	jwt("/pbac/resources/list", "pbac.resource-action", "list", accesscontrol.DataPermissionNone, pbacHandler.ListResources)
	jwt("/pbac/global-policies/create", "pbac.global-policy", "create", accesscontrol.DataPermissionNone, pbacHandler.Create)
	jwt("/pbac/global-policies/get", "pbac.global-policy", "read", accesscontrol.DataPermissionNone, pbacHandler.Get)
	jwt("/pbac/global-policies/page", "pbac.global-policy", "list", accesscontrol.DataPermissionNone, pbacHandler.Page)
	jwt("/pbac/global-policies/versions/create", "pbac.global-policy", "create-version", accesscontrol.DataPermissionNone, pbacHandler.CreateVersion)
	jwt("/pbac/global-policies/versions/get", "pbac.global-policy", "read", accesscontrol.DataPermissionNone, pbacHandler.GetVersion)
	jwt("/pbac/global-policies/versions/page", "pbac.global-policy", "list", accesscontrol.DataPermissionNone, pbacHandler.PageVersions)
	jwt("/pbac/global-policies/simulate", "pbac.global-policy", "simulate", accesscontrol.DataPermissionNone, pbacHandler.Simulate)
	jwt("/pbac/global-policies/publish", "pbac.global-policy", "publish", accesscontrol.DataPermissionNone, pbacHandler.Publish)
	jwt("/pbac/global-policies/status/set", "pbac.global-policy", "set-status", accesscontrol.DataPermissionNone, pbacHandler.SetStatus)
	jwt("/pbac/tenant-policies/create", "pbac.tenant-policy", "create", accesscontrol.DataPermissionNone, pbacHandler.Create)
	jwt("/pbac/tenant-policies/get", "pbac.tenant-policy", "read", accesscontrol.DataPermissionNone, pbacHandler.Get)
	jwt("/pbac/tenant-policies/page", "pbac.tenant-policy", "list", accesscontrol.DataPermissionNone, pbacHandler.Page)
	jwt("/pbac/tenant-policies/versions/create", "pbac.tenant-policy", "create-version", accesscontrol.DataPermissionNone, pbacHandler.CreateVersion)
	jwt("/pbac/tenant-policies/versions/get", "pbac.tenant-policy", "read", accesscontrol.DataPermissionNone, pbacHandler.GetVersion)
	jwt("/pbac/tenant-policies/versions/page", "pbac.tenant-policy", "list", accesscontrol.DataPermissionNone, pbacHandler.PageVersions)
	jwt("/pbac/tenant-policies/simulate", "pbac.tenant-policy", "simulate", accesscontrol.DataPermissionNone, pbacHandler.Simulate)
	jwt("/pbac/tenant-policies/publish", "pbac.tenant-policy", "publish", accesscontrol.DataPermissionNone, pbacHandler.Publish)
	jwt("/pbac/tenant-policies/status/set", "pbac.tenant-policy", "set-status", accesscontrol.DataPermissionNone, pbacHandler.SetStatus)
	jwt("/data-permissions/global-policies/create", "data-permission.global-policy", "create", accesscontrol.DataPermissionNone, dataPermissionHandler.Create)
	jwt("/data-permissions/global-policies/get", "data-permission.global-policy", "read", accesscontrol.DataPermissionNone, dataPermissionHandler.Get)
	jwt("/data-permissions/global-policies/page", "data-permission.global-policy", "list", accesscontrol.DataPermissionNone, dataPermissionHandler.Page)
	jwt("/data-permissions/global-policies/versions/create", "data-permission.global-policy", "create-version", accesscontrol.DataPermissionNone, dataPermissionHandler.CreateVersion)
	jwt("/data-permissions/global-policies/versions/get", "data-permission.global-policy", "read", accesscontrol.DataPermissionNone, dataPermissionHandler.GetVersion)
	jwt("/data-permissions/global-policies/versions/page", "data-permission.global-policy", "list", accesscontrol.DataPermissionNone, dataPermissionHandler.PageVersions)
	jwt("/data-permissions/global-policies/simulate", "data-permission.global-policy", "simulate", accesscontrol.DataPermissionNone, dataPermissionHandler.Simulate)
	jwt("/data-permissions/global-policies/publish", "data-permission.global-policy", "publish", accesscontrol.DataPermissionNone, dataPermissionHandler.Publish)
	jwt("/data-permissions/global-policies/status/set", "data-permission.global-policy", "set-status", accesscontrol.DataPermissionNone, dataPermissionHandler.SetStatus)
	jwt("/data-permissions/tenant-policies/create", "data-permission.tenant-policy", "create", accesscontrol.DataPermissionNone, dataPermissionHandler.Create)
	jwt("/data-permissions/tenant-policies/get", "data-permission.tenant-policy", "read", accesscontrol.DataPermissionNone, dataPermissionHandler.Get)
	jwt("/data-permissions/tenant-policies/page", "data-permission.tenant-policy", "list", accesscontrol.DataPermissionNone, dataPermissionHandler.Page)
	jwt("/data-permissions/tenant-policies/versions/create", "data-permission.tenant-policy", "create-version", accesscontrol.DataPermissionNone, dataPermissionHandler.CreateVersion)
	jwt("/data-permissions/tenant-policies/versions/get", "data-permission.tenant-policy", "read", accesscontrol.DataPermissionNone, dataPermissionHandler.GetVersion)
	jwt("/data-permissions/tenant-policies/versions/page", "data-permission.tenant-policy", "list", accesscontrol.DataPermissionNone, dataPermissionHandler.PageVersions)
	jwt("/data-permissions/tenant-policies/simulate", "data-permission.tenant-policy", "simulate", accesscontrol.DataPermissionNone, dataPermissionHandler.Simulate)
	jwt("/data-permissions/tenant-policies/publish", "data-permission.tenant-policy", "publish", accesscontrol.DataPermissionNone, dataPermissionHandler.Publish)
	jwt("/data-permissions/tenant-policies/status/set", "data-permission.tenant-policy", "set-status", accesscontrol.DataPermissionNone, dataPermissionHandler.SetStatus)
	jwt("/applications/create", "application", "create", accesscontrol.DataPermissionNone, applicationHandler.Create)
	jwt("/applications/get", "application", "read", accesscontrol.DataPermissionNone, applicationHandler.Get)
	jwt("/applications/page", "application", "list", accesscontrol.DataPermissionNone, applicationHandler.Page)
	jwt("/applications/update", "application", "update", accesscontrol.DataPermissionNone, applicationHandler.Update)
	jwt("/applications/delete", "application", "delete", accesscontrol.DataPermissionNone, applicationHandler.Delete)
	jwt("/platform/tenant-applications/grant", "tenant.application-grant", "grant", accesscontrol.DataPermissionNone, applicationHandler.GrantTenantApplication)
	jwt("/platform/tenant-applications/revoke", "tenant.application-grant", "revoke", accesscontrol.DataPermissionNone, applicationHandler.RevokeTenantApplication)
	jwt("/platform/tenant-applications/get", "tenant.application-grant", "read", accesscontrol.DataPermissionNone, applicationHandler.GetTenantApplication)
	jwt("/platform/tenant-applications/page", "tenant.application-grant", "list", accesscontrol.DataPermissionNone, applicationHandler.PageTenantApplications)
	jwt("/me/applications", "application.current", "list", accesscontrol.DataPermissionNone, applicationHandler.CurrentApplications)
	jwt("/me/application/current", "application.current", "read", accesscontrol.DataPermissionNone, applicationHandler.CurrentApplication)
	jwt("/me/application/switch", "application.current", "switch", accesscontrol.DataPermissionNone, applicationHandler.SwitchApplication)
	jwt("/navigations/create", "navigation", "create", accesscontrol.DataPermissionNone, navigationHandler.Create)
	jwt("/navigations/get", "navigation", "read", accesscontrol.DataPermissionNone, navigationHandler.Get)
	jwt("/navigations/tree", "navigation", "list", accesscontrol.DataPermissionNone, navigationHandler.Tree)
	jwt("/navigations/update", "navigation", "update", accesscontrol.DataPermissionNone, navigationHandler.Update)
	jwt("/navigations/delete", "navigation", "delete", accesscontrol.DataPermissionNone, navigationHandler.Delete)
	jwt("/me/navigations", "navigation.current", "read", accesscontrol.DataPermissionNone, navigationHandler.Current)
	jwt("/permissions/tree", "permission.definition", "list", accesscontrol.DataPermissionNone, permissionHandler.Tree)
	jwt("/permissions/create", "permission.definition", "create", accesscontrol.DataPermissionNone, permissionHandler.Create)
	jwt("/permissions/get", "permission.definition", "read", accesscontrol.DataPermissionNone, permissionHandler.Get)
	jwt("/permissions/update", "permission.definition", "update", accesscontrol.DataPermissionNone, permissionHandler.Update)
	jwt("/permissions/delete", "permission.definition", "delete", accesscontrol.DataPermissionNone, permissionHandler.Delete)
	jwt("/operation-logs/frontend/record", "operation.log", "create", accesscontrol.DataPermissionNone, operationLogHandler.RecordFrontend)
	jwt("/operation-logs/get", "operation.log", "read", accesscontrol.DataPermissionNone, operationLogHandler.Get)
	jwt("/operation-logs/page", "operation.log", "list", accesscontrol.DataPermissionNone, operationLogHandler.Page)
	jwt("/security-logs/get", "security.log", "read", accesscontrol.DataPermissionNone, securityLogHandler.Get)
	jwt("/security-logs/page", "security.log", "list", accesscontrol.DataPermissionNone, securityLogHandler.Page)
	if registrationErr != nil {
		return nil, fmt.Errorf("register HTTP authorization descriptor: %w", registrationErr)
	}
	endpointRegistry, err := accesscontrol.NewEndpointRegistry(resources, definitions)
	if err != nil {
		return nil, fmt.Errorf("build HTTP authorization registry: %w", err)
	}
	if err := endpointRegistry.ValidateCoverage(httpBusinessOperations(router)); err != nil {
		return nil, fmt.Errorf("validate HTTP authorization descriptor coverage: %w", err)
	}
	capabilityService.SetEndpointRegistry(endpointRegistry)
	server := &http.Server{Addr: cfg.HTTP.Address, Handler: router, ReadTimeout: cfg.HTTP.ReadTimeout, WriteTimeout: cfg.HTTP.WriteTimeout, IdleTimeout: cfg.HTTP.IdleTimeout}
	var listener net.Listener
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
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
	}, OnStop: server.Shutdown})
	return server, nil
}

func httpBusinessOperations(router *gin.Engine) []accesscontrol.Operation {
	if router == nil {
		return nil
	}
	operations := []accesscontrol.Operation{}
	for _, route := range router.Routes() {
		if strings.HasPrefix(route.Path, "/api/v1/") {
			operations = append(operations, accesscontrol.Operation{Transport: accesscontrol.TransportHTTP, Name: route.Method + " " + route.Path})
		}
	}
	return operations
}

func configureRouterContract(router *gin.Engine, logger *slog.Logger) {
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	router.HandleMethodNotAllowed = true
	router.NoRoute(func(c *gin.Context) { Fail(c, logger, apperror.NotFound("route not found")) })
	router.NoMethod(func(c *gin.Context) { Fail(c, logger, apperror.MethodNotAllowed()) })
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

var Module = fx.Module("http", fx.Provide(auth.NewRuntime, health.New, ratelimit.New, serviceaccount.New, NewHandler, NewFileHandler, NewUserHandler, NewServiceAccountHandler, NewTenantHandler, NewTenantMemberHandler, NewDepartmentHandler, NewTenantAuthorizationHandler, NewCapabilityHandler, NewApplicationHandler, NewNavigationHandler, NewPlatformConfigHandler, NewDictionaryHandler, NewPBACHandler, NewDataPermissionHandler, NewPermissionHandler, NewOperationLogHandler, NewSecurityLogHandler, NewAuthenticationHandler, NewUserAuthenticationHandler, NewServer), fx.Invoke(func(*http.Server) {}))
