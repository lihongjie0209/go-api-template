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
	"github.com/lihongjie0209/go-api-template/internal/ratelimit"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	"github.com/lihongjie0209/go-api-template/internal/serviceaccount"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.uber.org/fx"
)

var errHTTPServerStopped = errors.New("http_server_stopped")

func NewServer(lc fx.Lifecycle, cfg config.Config, handler *Handler, fileHandler *FileHandler, userHandler *UserHandler, serviceAccountHandler *ServiceAccountHandler, tenantHandler *TenantHandler, tenantMemberHandler *TenantMemberHandler, departmentHandler *DepartmentHandler, tenantAuthorizationHandler *TenantAuthorizationHandler, capabilityHandler *CapabilityHandler, capabilityService *authorization.CapabilityService, applicationHandler *ApplicationHandler, navigationHandler *NavigationHandler, platformConfigHandler *PlatformConfigHandler, scheduledJobHandler *ScheduledJobHandler, dictionaryHandler *DictionaryHandler, resourceCatalogHandler *ResourceCatalogHandler, platformGrantHandler *PlatformGrantHandler, dataPermissionHandler *DataPermissionHandler, permissionHandler *PermissionHandler, operationLogHandler *OperationLogHandler, securityLogHandler *SecurityLogHandler, authenticationHandler *AuthenticationHandler, userAuthenticationHandler *UserAuthenticationHandler, authService *auth.Service, resources *resourceaction.Registry, schemas *datapermission.SchemaRegistry, authorizer platformauthz.Authorizer, limiter *ratelimit.Limiter, idempotencyManager *idempotency.Manager, metrics *observability.Metrics, tracing *observability.Tracing, logger *slog.Logger) (*http.Server, error) {
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
	api := router.Group("/api/v1", RateLimit(limiter, cfg.RateLimit.IP, "ip", func(c *gin.Context) string { return c.ClientIP() }, logger), RateLimit(limiter, cfg.RateLimit.API, "api", func(c *gin.Context) string { return c.FullPath() }, logger), DatabaseAuthentication(authService, logger, cfg), PasswordChangeGate(logger), RateLimit(limiter, cfg.RateLimit.User, "user", func(c *gin.Context) string {
		value, _ := c.Get("subject")
		subject, _ := value.(string)
		return subject
	}, logger))
	idempotencyHandler := IdempotencyExecution(idempotencyManager, cfg.Idempotency.HTTPPaths, logger)
	var registrationErr error
	definitions := make([]accesscontrol.Endpoint, 0, 128)
	post := func(endpoint accesscontrol.Endpoint, handlers ...gin.HandlerFunc) {
		if registrationErr == nil {
			registrationErr = registerPOST(api, resources, schemas, authorizer, logger, idempotencyHandler, endpoint, handlers...)
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
	jwt("/platform/runtime/status", "platform.runtime", "read", accesscontrol.DataPermissionNone, handler.RuntimeStatus)
	jwt("/scheduled-jobs/create", "scheduled-job", "create", accesscontrol.DataPermissionNone, scheduledJobHandler.Create)
	jwt("/scheduled-jobs/get", "scheduled-job", "read", accesscontrol.DataPermissionNone, scheduledJobHandler.Get)
	jwt("/scheduled-jobs/page", "scheduled-job", "list", accesscontrol.DataPermissionNone, scheduledJobHandler.Page)
	jwt("/scheduled-jobs/update", "scheduled-job", "update", accesscontrol.DataPermissionNone, scheduledJobHandler.Update)
	jwt("/scheduled-jobs/update-target", "scheduled-job", "update", accesscontrol.DataPermissionNone, scheduledJobHandler.UpdateTarget)
	jwt("/scheduled-jobs/delete", "scheduled-job", "delete", accesscontrol.DataPermissionNone, scheduledJobHandler.Delete)
	jwt("/scheduled-jobs/delete-target", "scheduled-job", "delete", accesscontrol.DataPermissionNone, scheduledJobHandler.DeleteTarget)
	jwt("/scheduled-jobs/handlers/list", "scheduled-job", "list", accesscontrol.DataPermissionNone, scheduledJobHandler.ListHandlers)
	jwt("/scheduled-jobs/trigger", "scheduled-job", "execute", accesscontrol.DataPermissionNone, scheduledJobHandler.Trigger)
	jwt("/scheduled-job-runs/get", "scheduled-job", "read", accesscontrol.DataPermissionNone, scheduledJobHandler.GetRun)
	jwt("/scheduled-job-runs/page", "scheduled-job", "list", accesscontrol.DataPermissionNone, scheduledJobHandler.PageRuns)
	public("/.well-known/jwks.json", handler.JWKSAPI)
	public("/auth/login", RateLimit(limiter, cfg.RateLimit.Login, "login", func(c *gin.Context) string { return c.ClientIP() }, logger), authenticationHandler.Login)
	public("/auth/user/login", RateLimit(limiter, cfg.RateLimit.Login, "user-login", func(c *gin.Context) string { return c.ClientIP() }, logger), userAuthenticationHandler.Login)
	public("/auth/refresh", userAuthenticationHandler.Refresh)
	public("/auth/logout", userAuthenticationHandler.Logout)
	jwt("/auth/password/reset", "identity.user", "reset-password", accesscontrol.DataPermissionNone, userAuthenticationHandler.SetPassword)
	jwt("/auth/password/change", "identity.credential", "change-password", accesscontrol.DataPermissionNone, userAuthenticationHandler.ChangePassword)
	jwt("/auth/sessions/page", "identity.session", "list", accesscontrol.DataPermissionNone, userAuthenticationHandler.Sessions)
	jwt("/auth/sessions/revoke", "identity.session", "revoke", accesscontrol.DataPermissionNone, userAuthenticationHandler.RevokeSession)
	jwt("/auth/sessions/revoke-target", "identity.session", "revoke", accesscontrol.DataPermissionNone, userAuthenticationHandler.SessionRevokeTarget)
	jwt("/auth/sessions/logout-all", "identity.session", "logout", accesscontrol.DataPermissionNone, userAuthenticationHandler.LogoutAll)
	jwt("/auth/sessions/force-logout-all", "identity.user", "force-logout", accesscontrol.DataPermissionNone, userAuthenticationHandler.ForceLogoutAll)
	jwt("/users/security-target/reset-password", "identity.user", "reset-password", accesscontrol.DataPermissionNone, userHandler.ResetPasswordTarget)
	jwt("/users/security-target/force-logout", "identity.user", "force-logout", accesscontrol.DataPermissionNone, userHandler.ForceLogoutTarget)
	jwt("/me", "identity.profile", "read", accesscontrol.DataPermissionNone, handler.Me)
	jwt("/profile/get", "identity.profile", "read", accesscontrol.DataPermissionNone, userHandler.GetProfile)
	jwt("/profile/update", "identity.profile", "update", accesscontrol.DataPermissionNone, userHandler.UpdateProfile)
	jwt("/authorization/capabilities/evaluate", "authorization.capability", "evaluate", accesscontrol.DataPermissionNone, capabilityHandler.Evaluate)
	jwt("/authorization/rows/evaluate", "authorization.capability", "evaluate", accesscontrol.DataPermissionNone, capabilityHandler.EvaluateRows)
	public("/example/ping", handler.Ping)
	jwt("/files/upload", "file.object", "create", accesscontrol.DataPermissionNone, fileHandler.Upload)
	jwt("/files/get", "file.object", "read", accesscontrol.DataPermissionNone, fileHandler.Get)
	jwt("/files/page", "file.object", "list", accesscontrol.DataPermissionNone, fileHandler.Page)
	jwt("/files/download", "file.object", "download", accesscontrol.DataPermissionNone, fileHandler.Download)
	jwt("/files/delete", "file.object", "delete", accesscontrol.DataPermissionNone, fileHandler.Delete)
	jwt("/files/delete-target", "file.object", "delete", accesscontrol.DataPermissionNone, fileHandler.DeleteTarget)
	jwt("/users/create", "identity.user", "create", accesscontrol.DataPermissionNone, userHandler.Create)
	jwt("/users/get", "identity.user", "read", accesscontrol.DataPermissionNone, userHandler.Get)
	jwt("/users/page", "identity.user", "list", accesscontrol.DataPermissionNone, userHandler.Page)
	jwt("/users/update", "identity.user", "update", accesscontrol.DataPermissionNone, userHandler.Update)
	jwt("/users/update-target", "identity.user", "update", accesscontrol.DataPermissionNone, userHandler.UpdateTarget)
	jwt("/users/delete", "identity.user", "delete", accesscontrol.DataPermissionNone, userHandler.Delete)
	jwt("/users/delete-target", "identity.user", "delete", accesscontrol.DataPermissionNone, userHandler.DeleteTarget)
	jwt("/service-accounts/create", "identity.service-account", "create", accesscontrol.DataPermissionNone, serviceAccountHandler.Create)
	jwt("/service-accounts/get", "identity.service-account", "read", accesscontrol.DataPermissionNone, serviceAccountHandler.Get)
	jwt("/service-accounts/page", "identity.service-account", "list", accesscontrol.DataPermissionNone, serviceAccountHandler.Page)
	jwt("/service-accounts/update", "identity.service-account", "update", accesscontrol.DataPermissionNone, serviceAccountHandler.Update)
	jwt("/service-accounts/update-target", "identity.service-account", "update", accesscontrol.DataPermissionNone, serviceAccountHandler.UpdateTarget)
	jwt("/service-accounts/secret/rotate", "identity.service-account", "rotate-secret", accesscontrol.DataPermissionNone, serviceAccountHandler.RotateSecret)
	jwt("/service-accounts/secret/rotate-target", "identity.service-account", "rotate-secret", accesscontrol.DataPermissionNone, serviceAccountHandler.RotateTarget)
	jwt("/service-accounts/delete", "identity.service-account", "delete", accesscontrol.DataPermissionNone, serviceAccountHandler.Delete)
	jwt("/service-accounts/delete-target", "identity.service-account", "delete", accesscontrol.DataPermissionNone, serviceAccountHandler.DeleteTarget)
	jwt("/tenants/get", "tenant.profile", "read", accesscontrol.DataPermissionNone, tenantHandler.Get)
	jwt("/tenants/page", "tenant.profile", "list", accesscontrol.DataPermissionNone, tenantHandler.Page)
	jwt("/tenants/update", "tenant.profile", "update", accesscontrol.DataPermissionNone, tenantHandler.Update)
	jwt("/tenants/delete", "tenant.profile", "delete", accesscontrol.DataPermissionNone, tenantHandler.Delete)
	jwt("/tenants/delete-target", "tenant.profile", "delete", accesscontrol.DataPermissionNone, tenantHandler.DeleteTarget)
	jwt("/tenants/update-target", "tenant.profile", "update", accesscontrol.DataPermissionNone, tenantHandler.UpdateTarget)
	jwt("/platform/tenants/create", "tenant", "create", accesscontrol.DataPermissionNone, tenantHandler.Create)
	jwt("/platform/tenants/get", "tenant", "read", accesscontrol.DataPermissionNone, tenantHandler.AdminGet)
	jwt("/platform/tenants/page", "tenant", "list", accesscontrol.DataPermissionNone, tenantHandler.AdminPage)
	jwt("/platform/tenants/update", "tenant", "update", accesscontrol.DataPermissionNone, tenantHandler.AdminUpdate)
	jwt("/platform/tenants/update-target", "tenant", "update", accesscontrol.DataPermissionNone, tenantHandler.AdminUpdateTarget)
	jwt("/platform/tenants/delete", "tenant", "delete", accesscontrol.DataPermissionNone, tenantHandler.AdminDelete)
	jwt("/platform/tenants/delete-target", "tenant", "delete", accesscontrol.DataPermissionNone, tenantHandler.AdminDeleteTarget)
	jwt("/tenant-members/add", "tenant.member", "add", accesscontrol.DataPermissionObject, tenantMemberHandler.Add)
	jwt("/tenant-members/get", "tenant.member", "read", accesscontrol.DataPermissionRequired, tenantMemberHandler.Get)
	jwt("/tenant-members/page", "tenant.member", "list", accesscontrol.DataPermissionRequired, tenantMemberHandler.Page)
	jwt("/tenant-members/status/update", "tenant.member", "update", accesscontrol.DataPermissionRequired, tenantMemberHandler.UpdateStatus)
	jwt("/tenant-members/status/target", "tenant.member", "update", accesscontrol.DataPermissionRequired, tenantMemberHandler.StatusTarget)
	jwt("/tenant-members/remove", "tenant.member", "remove", accesscontrol.DataPermissionRequired, tenantMemberHandler.Remove)
	jwt("/tenant-members/remove-target", "tenant.member", "remove", accesscontrol.DataPermissionRequired, tenantMemberHandler.RemoveTarget)
	jwt("/tenant-context/available", "tenant.selection", "list", accesscontrol.DataPermissionNone, tenantMemberHandler.AvailableTenants)
	jwt("/tenant-context/switch", "tenant.selection", "switch", accesscontrol.DataPermissionNone, tenantMemberHandler.SwitchTenant)
	jwt("/tenant-context/current", "tenant.context", "read", accesscontrol.DataPermissionNone, tenantMemberHandler.CurrentTenant)
	jwt("/tenant-departments/create", "tenant.department", "create", accesscontrol.DataPermissionObject, departmentHandler.Create)
	jwt("/tenant-departments/get", "tenant.department", "read", accesscontrol.DataPermissionRequired, departmentHandler.Get)
	jwt("/tenant-departments/update-target", "tenant.department", "update", accesscontrol.DataPermissionRequired, departmentHandler.UpdateTarget)
	jwt("/tenant-departments/delete-target", "tenant.department", "delete", accesscontrol.DataPermissionRequired, departmentHandler.DeleteTarget)
	jwt("/tenant-departments/tree", "tenant.department", "list", accesscontrol.DataPermissionRequired, departmentHandler.Tree)
	jwt("/tenant-departments/update", "tenant.department", "update", accesscontrol.DataPermissionRequired, departmentHandler.Update)
	jwt("/tenant-departments/delete", "tenant.department", "delete", accesscontrol.DataPermissionRequired, departmentHandler.Delete)
	jwt("/tenant-departments/members/set", "tenant.department", "assign-member", accesscontrol.DataPermissionRequired, departmentHandler.SetMembers)
	jwt("/tenant-departments/members/get", "tenant.department", "assign-member", accesscontrol.DataPermissionRequired, departmentHandler.Members)
	jwt("/tenant-departments/members/target", "tenant.department", "assign-member", accesscontrol.DataPermissionRequired, departmentHandler.AssignmentTarget)
	jwt("/platform/tenant-authorization/permissions/set", "tenant", "grant", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.SetTenantPermissions)
	jwt("/platform/tenant-authorization/permissions/get", "tenant", "grant", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.TenantPermissionCeiling)
	jwt("/platform/tenant-authorization/permissions/target", "tenant", "grant", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.TenantPermissionTarget)
	jwt("/platform/tenant-authorization/permissions/page", "tenant", "grant", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.PageTenantGrantablePermissions)
	jwt("/platform/tenant-authorization/administrators/page", "tenant", "assign-administrator", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.PagePlatformAdministratorCandidates)
	jwt("/platform/tenant-authorization/administrators/target", "tenant", "assign-administrator", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.PlatformAdministratorTarget)
	jwt("/platform/tenant-authorization/administrators/set", "tenant", "assign-administrator", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.SetAdministrator)
	jwt("/tenant-authorization/administrators/page", "tenant.authorization", "assign-administrator", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.PageTenantAdministratorCandidates)
	jwt("/tenant-authorization/administrators/set", "tenant.authorization", "assign-administrator", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.SetAdministrator)
	jwt("/tenant-authorization/effective-permissions", "tenant.authorization", "read", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.EffectivePermissions)
	jwt("/tenant-authorization/assignable-permissions", "tenant.authorization", "read", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.AssignablePermissions)
	jwt("/tenant-authorization/assignable-permissions/page", "tenant.authorization", "read", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.PageAssignablePermissions)
	jwt("/tenant-roles/create", "tenant.role", "create", accesscontrol.DataPermissionObject, tenantAuthorizationHandler.CreateRole)
	jwt("/tenant-roles/get", "tenant.role", "read", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.GetRole)
	jwt("/tenant-roles/update-target", "tenant.role", "update", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.RoleUpdateTarget)
	jwt("/tenant-roles/delete-target", "tenant.role", "delete", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.RoleDeleteTarget)
	jwt("/tenant-roles/page", "tenant.role", "list", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.PageRoles)
	jwt("/tenant-roles/assignment-candidates/page", "tenant.role", "list", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.PageRoleAssignmentCandidates)
	jwt("/tenant-roles/update", "tenant.role", "update", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.UpdateRole)
	jwt("/tenant-roles/delete", "tenant.role", "delete", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.DeleteRole)
	jwt("/tenant-roles/permissions/get", "tenant.role", "read", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.RolePermissions)
	jwt("/tenant-roles/permissions/target", "tenant.role", "grant", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.RoleGrantTarget)
	jwt("/tenant-roles/permissions/set", "tenant.role", "grant", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.SetRolePermissions)
	jwt("/tenant-members/roles/set", "tenant.member", "assign-role", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.SetMemberRoles)
	jwt("/tenant-members/roles/target", "tenant.member", "assign-role", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.MemberRoleAssignmentTarget)
	jwt("/tenant-members/roles/get", "tenant.member", "read", accesscontrol.DataPermissionRequired, tenantAuthorizationHandler.MemberRoles)
	jwt("/platform-configs/create", "platform.config", "create", accesscontrol.DataPermissionNone, platformConfigHandler.Create)
	jwt("/platform-configs/get", "platform.config", "read", accesscontrol.DataPermissionNone, platformConfigHandler.Get)
	jwt("/platform-configs/page", "platform.config", "list", accesscontrol.DataPermissionNone, platformConfigHandler.Page)
	jwt("/platform-configs/update", "platform.config", "update", accesscontrol.DataPermissionNone, platformConfigHandler.Update)
	jwt("/platform-configs/update-target", "platform.config", "update", accesscontrol.DataPermissionNone, platformConfigHandler.UpdateTarget)
	jwt("/platform-configs/delete", "platform.config", "delete", accesscontrol.DataPermissionNone, platformConfigHandler.Delete)
	jwt("/platform-configs/delete-target", "platform.config", "delete", accesscontrol.DataPermissionNone, platformConfigHandler.DeleteTarget)
	public("/public/platform-configs/get", platformConfigHandler.GetPublic)
	public("/public/platform-configs/list", platformConfigHandler.ListPublic)
	public("/public/dictionaries/query", dictionaryHandler.Query)
	jwt("/dictionaries/create", "dictionary.definition", "create", accesscontrol.DataPermissionNone, dictionaryHandler.CreateDefinition)
	jwt("/dictionaries/get", "dictionary.definition", "read", accesscontrol.DataPermissionNone, dictionaryHandler.GetDefinition)
	jwt("/dictionaries/update-target", "dictionary.definition", "update", accesscontrol.DataPermissionNone, dictionaryHandler.UpdateTarget)
	jwt("/dictionaries/delete-target", "dictionary.definition", "delete", accesscontrol.DataPermissionNone, dictionaryHandler.DeleteTarget)
	jwt("/dictionaries/page", "dictionary.definition", "list", accesscontrol.DataPermissionNone, dictionaryHandler.PageDefinitions)
	jwt("/dictionaries/update", "dictionary.definition", "update", accesscontrol.DataPermissionNone, dictionaryHandler.UpdateDefinition)
	jwt("/dictionaries/delete", "dictionary.definition", "delete", accesscontrol.DataPermissionNone, dictionaryHandler.DeleteDefinition)
	jwt("/authorization/resources/create", "authorization.resource", "create", accesscontrol.DataPermissionNone, resourceCatalogHandler.CreateResource)
	jwt("/authorization/resources/get", "authorization.resource", "read", accesscontrol.DataPermissionNone, resourceCatalogHandler.GetResource)
	jwt("/authorization/resources/page", "authorization.resource", "list", accesscontrol.DataPermissionNone, resourceCatalogHandler.PageResources)
	jwt("/authorization/resources/update-target", "authorization.resource", "update", accesscontrol.DataPermissionNone, resourceCatalogHandler.ResourceUpdateTarget)
	jwt("/authorization/resources/delete-target", "authorization.resource", "delete", accesscontrol.DataPermissionNone, resourceCatalogHandler.ResourceDeleteTarget)
	jwt("/authorization/resources/update", "authorization.resource", "update", accesscontrol.DataPermissionNone, resourceCatalogHandler.UpdateResource)
	jwt("/authorization/resources/delete", "authorization.resource", "delete", accesscontrol.DataPermissionNone, resourceCatalogHandler.DeleteResource)
	jwt("/authorization/actions/create", "authorization.action", "create", accesscontrol.DataPermissionNone, resourceCatalogHandler.CreateAction)
	jwt("/authorization/actions/get", "authorization.action", "read", accesscontrol.DataPermissionNone, resourceCatalogHandler.GetAction)
	jwt("/authorization/actions/page", "authorization.action", "list", accesscontrol.DataPermissionNone, resourceCatalogHandler.PageActions)
	jwt("/authorization/actions/update-target", "authorization.action", "update", accesscontrol.DataPermissionNone, resourceCatalogHandler.ActionUpdateTarget)
	jwt("/authorization/actions/delete-target", "authorization.action", "delete", accesscontrol.DataPermissionNone, resourceCatalogHandler.ActionDeleteTarget)
	jwt("/authorization/actions/update", "authorization.action", "update", accesscontrol.DataPermissionNone, resourceCatalogHandler.UpdateAction)
	jwt("/authorization/actions/delete", "authorization.action", "delete", accesscontrol.DataPermissionNone, resourceCatalogHandler.DeleteAction)
	jwt("/platform/permission-grants/create", "authorization.platform-grant", "create", accesscontrol.DataPermissionNone, platformGrantHandler.Create)
	jwt("/platform/permission-grants/get", "authorization.platform-grant", "read", accesscontrol.DataPermissionNone, platformGrantHandler.Get)
	jwt("/platform/permission-grants/page", "authorization.platform-grant", "list", accesscontrol.DataPermissionNone, platformGrantHandler.Page)
	jwt("/platform/permission-grants/update-target", "authorization.platform-grant", "update", accesscontrol.DataPermissionNone, platformGrantHandler.UpdateTarget)
	jwt("/platform/permission-grants/delete-target", "authorization.platform-grant", "delete", accesscontrol.DataPermissionNone, platformGrantHandler.DeleteTarget)
	jwt("/platform/permission-grants/permissions/page", "permission.definition", "list", accesscontrol.DataPermissionNone, platformGrantHandler.PageGrantablePermissions)
	jwt("/platform/permission-grants/update", "authorization.platform-grant", "update", accesscontrol.DataPermissionNone, platformGrantHandler.Update)
	jwt("/platform/permission-grants/delete", "authorization.platform-grant", "delete", accesscontrol.DataPermissionNone, platformGrantHandler.Delete)
	jwt("/permissions/actions/get", "permission.definition", "read", accesscontrol.DataPermissionNone, permissionHandler.GetActions)
	jwt("/permissions/actions/target", "permission.definition", "update", accesscontrol.DataPermissionNone, permissionHandler.ActionEditTarget)
	jwt("/permissions/actions/set", "permission.definition", "update", accesscontrol.DataPermissionNone, permissionHandler.SetActions)
	jwt("/dictionary-items/create", "dictionary.item", "create", accesscontrol.DataPermissionNone, dictionaryHandler.CreateItem)
	jwt("/dictionary-items/get", "dictionary.item", "read", accesscontrol.DataPermissionNone, dictionaryHandler.GetItem)
	jwt("/dictionary-items/update-target", "dictionary.item", "update", accesscontrol.DataPermissionNone, dictionaryHandler.ItemUpdateTarget)
	jwt("/dictionary-items/delete-target", "dictionary.item", "delete", accesscontrol.DataPermissionNone, dictionaryHandler.ItemDeleteTarget)
	jwt("/dictionary-items/page", "dictionary.item", "list", accesscontrol.DataPermissionNone, dictionaryHandler.PageItems)
	jwt("/dictionary-items/definition-target/read", "dictionary.item", "read", accesscontrol.DataPermissionNone, dictionaryHandler.ItemDefinitionTargetRead)
	jwt("/dictionary-items/definition-target/list", "dictionary.item", "list", accesscontrol.DataPermissionNone, dictionaryHandler.ItemDefinitionTargetList)
	jwt("/dictionary-items/definition-target/create", "dictionary.item", "create", accesscontrol.DataPermissionNone, dictionaryHandler.ItemDefinitionTargetCreate)
	jwt("/dictionary-items/definition-target/update", "dictionary.item", "update", accesscontrol.DataPermissionNone, dictionaryHandler.ItemDefinitionTargetUpdate)
	jwt("/dictionary-items/definition-target/delete", "dictionary.item", "delete", accesscontrol.DataPermissionNone, dictionaryHandler.ItemDefinitionTargetDelete)
	jwt("/dictionary-items/update", "dictionary.item", "update", accesscontrol.DataPermissionNone, dictionaryHandler.UpdateItem)
	jwt("/dictionary-items/delete", "dictionary.item", "delete", accesscontrol.DataPermissionNone, dictionaryHandler.DeleteItem)
	jwt("/data-permissions/global-policies/create", "data-permission.global-policy", "create", accesscontrol.DataPermissionNone, dataPermissionHandler.Create)
	jwt("/data-permissions/global-policies/get", "data-permission.global-policy", "read", accesscontrol.DataPermissionNone, dataPermissionHandler.Get)
	jwt("/data-permissions/global-policies/page", "data-permission.global-policy", "list", accesscontrol.DataPermissionNone, dataPermissionHandler.Page)
	jwt("/data-permissions/global-policies/versions/create", "data-permission.global-policy", "create-version", accesscontrol.DataPermissionNone, dataPermissionHandler.CreateVersion)
	jwt("/data-permissions/global-policies/versions/create-target", "data-permission.global-policy", "create-version", accesscontrol.DataPermissionNone, dataPermissionHandler.VersionCreateTarget)
	jwt("/data-permissions/global-policies/versions/get", "data-permission.global-policy", "read", accesscontrol.DataPermissionNone, dataPermissionHandler.GetVersion)
	jwt("/data-permissions/global-policies/versions/page", "data-permission.global-policy", "list", accesscontrol.DataPermissionNone, dataPermissionHandler.PageVersions)
	jwt("/data-permissions/global-policies/versions/simulate", "data-permission.global-policy", "simulate", accesscontrol.DataPermissionNone, dataPermissionHandler.SimulateVersion)
	jwt("/data-permissions/global-policies/simulate", "data-permission.global-policy", "simulate", accesscontrol.DataPermissionNone, dataPermissionHandler.Simulate)
	jwt("/data-permissions/global-policies/publish", "data-permission.global-policy", "publish", accesscontrol.DataPermissionNone, dataPermissionHandler.Publish)
	jwt("/data-permissions/global-policies/publish/target", "data-permission.global-policy", "publish", accesscontrol.DataPermissionNone, dataPermissionHandler.PublishTarget)
	jwt("/data-permissions/global-policies/status/set", "data-permission.global-policy", "set-status", accesscontrol.DataPermissionNone, dataPermissionHandler.SetStatus)
	jwt("/data-permissions/global-policies/status/target", "data-permission.global-policy", "set-status", accesscontrol.DataPermissionNone, dataPermissionHandler.StatusTarget)
	jwt("/data-permissions/tenant-policies/create", "data-permission.tenant-policy", "create", accesscontrol.DataPermissionNone, dataPermissionHandler.Create)
	jwt("/data-permissions/tenant-policies/create/context", "data-permission.tenant-policy", "create", accesscontrol.DataPermissionNone, dataPermissionHandler.CreateContext)
	jwt("/data-permissions/tenant-policies/get", "data-permission.tenant-policy", "read", accesscontrol.DataPermissionNone, dataPermissionHandler.Get)
	jwt("/data-permissions/tenant-policies/page", "data-permission.tenant-policy", "list", accesscontrol.DataPermissionNone, dataPermissionHandler.Page)
	jwt("/data-permissions/tenant-policies/versions/create", "data-permission.tenant-policy", "create-version", accesscontrol.DataPermissionNone, dataPermissionHandler.CreateVersion)
	jwt("/data-permissions/tenant-policies/versions/create-target", "data-permission.tenant-policy", "create-version", accesscontrol.DataPermissionNone, dataPermissionHandler.VersionCreateTarget)
	jwt("/data-permissions/tenant-policies/versions/get", "data-permission.tenant-policy", "read", accesscontrol.DataPermissionNone, dataPermissionHandler.GetVersion)
	jwt("/data-permissions/tenant-policies/versions/page", "data-permission.tenant-policy", "list", accesscontrol.DataPermissionNone, dataPermissionHandler.PageVersions)
	jwt("/data-permissions/tenant-policies/versions/simulate", "data-permission.tenant-policy", "simulate", accesscontrol.DataPermissionNone, dataPermissionHandler.SimulateVersion)
	jwt("/data-permissions/tenant-policies/simulate", "data-permission.tenant-policy", "simulate", accesscontrol.DataPermissionNone, dataPermissionHandler.Simulate)
	jwt("/data-permissions/tenant-policies/simulate/context", "data-permission.tenant-policy", "simulate", accesscontrol.DataPermissionNone, dataPermissionHandler.SimulationContext)
	jwt("/data-permissions/tenant-policies/publish", "data-permission.tenant-policy", "publish", accesscontrol.DataPermissionNone, dataPermissionHandler.Publish)
	jwt("/data-permissions/tenant-policies/publish/target", "data-permission.tenant-policy", "publish", accesscontrol.DataPermissionNone, dataPermissionHandler.PublishTarget)
	jwt("/data-permissions/tenant-policies/status/set", "data-permission.tenant-policy", "set-status", accesscontrol.DataPermissionNone, dataPermissionHandler.SetStatus)
	jwt("/data-permissions/tenant-policies/status/target", "data-permission.tenant-policy", "set-status", accesscontrol.DataPermissionNone, dataPermissionHandler.StatusTarget)
	jwt("/applications/create", "application", "create", accesscontrol.DataPermissionNone, applicationHandler.Create)
	jwt("/applications/get", "application", "read", accesscontrol.DataPermissionNone, applicationHandler.Get)
	jwt("/applications/page", "application", "list", accesscontrol.DataPermissionNone, applicationHandler.Page)
	jwt("/applications/update", "application", "update", accesscontrol.DataPermissionNone, applicationHandler.Update)
	jwt("/applications/update-target", "application", "update", accesscontrol.DataPermissionNone, applicationHandler.UpdateTarget)
	jwt("/applications/delete", "application", "delete", accesscontrol.DataPermissionNone, applicationHandler.Delete)
	jwt("/applications/delete-target", "application", "delete", accesscontrol.DataPermissionNone, applicationHandler.DeleteTarget)
	jwt("/platform/tenant-applications/grant", "tenant.application-grant", "grant", accesscontrol.DataPermissionNone, applicationHandler.GrantTenantApplication)
	jwt("/platform/tenant-applications/revoke", "tenant.application-grant", "revoke", accesscontrol.DataPermissionNone, applicationHandler.RevokeTenantApplication)
	jwt("/platform/tenant-applications/revoke-target", "tenant.application-grant", "revoke", accesscontrol.DataPermissionNone, applicationHandler.RevokeTenantApplicationTarget)
	jwt("/platform/tenant-applications/get", "tenant.application-grant", "read", accesscontrol.DataPermissionNone, applicationHandler.GetTenantApplication)
	jwt("/platform/tenant-applications/page", "tenant.application-grant", "list", accesscontrol.DataPermissionNone, applicationHandler.PageTenantApplications)
	jwt("/platform/tenant-applications/target/read", "tenant.application-grant", "read", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.TenantApplicationTargetRead)
	jwt("/platform/tenant-applications/target/list", "tenant.application-grant", "list", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.TenantApplicationTargetList)
	jwt("/platform/tenant-applications/target/grant", "tenant.application-grant", "grant", accesscontrol.DataPermissionNone, tenantAuthorizationHandler.TenantApplicationTargetGrant)
	jwt("/me/applications", "application.current", "list", accesscontrol.DataPermissionNone, applicationHandler.CurrentApplications)
	jwt("/me/application/current", "application.current", "read", accesscontrol.DataPermissionNone, applicationHandler.CurrentApplication)
	jwt("/me/application/switch", "application.current", "switch", accesscontrol.DataPermissionNone, applicationHandler.SwitchApplication)
	jwt("/navigations/create", "navigation", "create", accesscontrol.DataPermissionNone, navigationHandler.Create)
	jwt("/navigations/get", "navigation", "read", accesscontrol.DataPermissionNone, navigationHandler.Get)
	jwt("/navigations/tree", "navigation", "list", accesscontrol.DataPermissionNone, navigationHandler.Tree)
	jwt("/navigations/application-target/read", "navigation", "read", accesscontrol.DataPermissionNone, applicationHandler.NavigationApplicationTargetRead)
	jwt("/navigations/application-target/list", "navigation", "list", accesscontrol.DataPermissionNone, applicationHandler.NavigationApplicationTargetList)
	jwt("/navigations/application-target/create", "navigation", "create", accesscontrol.DataPermissionNone, applicationHandler.NavigationApplicationTargetCreate)
	jwt("/navigations/update-target", "navigation", "update", accesscontrol.DataPermissionNone, navigationHandler.UpdateTarget)
	jwt("/navigations/delete-target", "navigation", "delete", accesscontrol.DataPermissionNone, navigationHandler.DeleteTarget)
	jwt("/navigations/update", "navigation", "update", accesscontrol.DataPermissionNone, navigationHandler.Update)
	jwt("/navigations/delete", "navigation", "delete", accesscontrol.DataPermissionNone, navigationHandler.Delete)
	jwt("/me/navigations", "navigation.current", "read", accesscontrol.DataPermissionNone, navigationHandler.Current)
	jwt("/permissions/tree", "permission.definition", "list", accesscontrol.DataPermissionNone, permissionHandler.Tree)
	jwt("/permissions/create", "permission.definition", "create", accesscontrol.DataPermissionNone, permissionHandler.Create)
	jwt("/permissions/get", "permission.definition", "read", accesscontrol.DataPermissionNone, permissionHandler.Get)
	jwt("/permissions/update-target", "permission.definition", "update", accesscontrol.DataPermissionNone, permissionHandler.UpdateTarget)
	jwt("/permissions/delete-target", "permission.definition", "delete", accesscontrol.DataPermissionNone, permissionHandler.DeleteTarget)
	jwt("/permissions/update", "permission.definition", "update", accesscontrol.DataPermissionNone, permissionHandler.Update)
	jwt("/permissions/delete", "permission.definition", "delete", accesscontrol.DataPermissionNone, permissionHandler.Delete)
	jwt("/operation-logs/frontend/record", "frontend.telemetry", "record", accesscontrol.DataPermissionNone, operationLogHandler.RecordFrontend)
	jwt("/me/navigation-usage", "navigation.current", "read", accesscontrol.DataPermissionNone, operationLogHandler.NavigationUsage)
	jwt("/operation-logs/get", "operation.log", "read", accesscontrol.DataPermissionNone, operationLogHandler.Get)
	jwt("/operation-logs/page", "operation.log", "list", accesscontrol.DataPermissionNone, operationLogHandler.Page)
	jwt("/security-logs/get", "security.log", "read", accesscontrol.DataPermissionNone, securityLogHandler.Get)
	jwt("/security-logs/page", "security.log", "list", accesscontrol.DataPermissionNone, securityLogHandler.Page)
	jwt("/platform/operation-logs/get", "platform.operation-log", "read", accesscontrol.DataPermissionNone, operationLogHandler.SystemGet)
	jwt("/platform/operation-logs/page", "platform.operation-log", "list", accesscontrol.DataPermissionNone, operationLogHandler.SystemPage)
	jwt("/platform/security-logs/get", "platform.security-log", "read", accesscontrol.DataPermissionNone, securityLogHandler.SystemGet)
	jwt("/platform/security-logs/page", "platform.security-log", "list", accesscontrol.DataPermissionNone, securityLogHandler.SystemPage)
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
		go serveHTTP(server, listener, logger)
		logger.Info("http server started", "address", server.Addr)
		return nil
	}, OnStop: server.Shutdown})
	return server, nil
}

func serveHTTP(server *http.Server, listener net.Listener, logger *slog.Logger) {
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server stopped unexpectedly", "error", observability.SafeError(err, errHTTPServerStopped))
	}
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

var Module = fx.Module("http", fx.Provide(auth.NewRuntime, health.New, ratelimit.New, serviceaccount.New, NewHandler, NewFileHandler, NewUserHandler, NewServiceAccountHandler, NewTenantHandler, NewTenantMemberHandler, NewDepartmentHandler, NewTenantAuthorizationHandler, NewCapabilityHandler, NewApplicationHandler, NewNavigationHandler, NewPlatformConfigHandler, NewScheduledJobHandler, NewDictionaryHandler, NewResourceCatalogHandler, NewPlatformGrantHandler, NewDataPermissionHandler, NewPermissionHandler, NewOperationLogHandler, NewSecurityLogHandler, NewAuthenticationHandler, NewUserAuthenticationHandler, NewServer), fx.Invoke(func(*http.Server) {}))
