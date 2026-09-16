package httptransport

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
)

func publicEndpoint(path string) accesscontrol.Endpoint {
	return accesscontrol.Endpoint{Transport: accesscontrol.TransportHTTP, Operation: "POST " + path, Authentication: accesscontrol.AuthenticationPublic, DataPermission: accesscontrol.DataPermissionNone}
}

func jwtEndpoint(path, resource, action string, data accesscontrol.DataPermissionMode) accesscontrol.Endpoint {
	reason := ""
	if data == accesscontrol.DataPermissionNone {
		reason = dataPermissionExemption(resource, action)
	}
	return accesscontrol.Endpoint{Transport: accesscontrol.TransportHTTP, Operation: "POST " + path, Authentication: accesscontrol.AuthenticationJWT, Resource: resource, Action: action, DataPermission: data, DataPermissionReason: reason}
}

func dataPermissionExemption(resource, action string) string {
	switch resource {
	case "identity.user", "identity.service-account", "tenant", "permission.definition", "menu", "dictionary.definition", "dictionary.item", "platform.config", "pbac.resource-action", "pbac.global-policy", "data-permission.global-policy":
		return "platform-scoped resource; operation PBAC is the complete authorization boundary"
	case "identity.profile", "identity.session", "identity.credential", "tenant.selection":
		return "service enforces the authenticated principal ownership predicate"
	case "tenant.profile":
		return "service enforces the authenticated principal tenant boundary"
	case "tenant.context", "tenant.authorization":
		return "service enforces the current tenant and membership boundary"
	case "menu.current":
		return "service derives visibility from the current tenant effective permissions"
	case "file.object", "operation.log", "security.log":
		return "resource is intentionally tenant-wide and repository SQL enforces tenant isolation"
	case "pbac.tenant-policy", "data-permission.tenant-policy":
		return "policy lifecycle repository enforces its intrinsic tenant boundary"
	case "tenant.member":
		if action == "add" {
			return "create operation has no existing row; service derives and validates the tenant boundary"
		}
	case "tenant.department", "tenant.role":
		if action == "create" {
			return "create operation has no existing row; service derives and validates the tenant boundary"
		}
	}
	return ""
}

// registerPOST binds the handler and its authorization contract atomically.
func registerPOST(group *gin.RouterGroup, resources *pbac.Registry, schemas *datapermission.SchemaRegistry, authorizer platformauthz.Authorizer, logger *slog.Logger, endpoint accesscontrol.Endpoint, handlers ...gin.HandlerFunc) error {
	if err := schemas.ValidateEndpoint(endpoint); err != nil {
		return err
	}
	registry, err := accesscontrol.NewEndpointRegistry(resources, []accesscontrol.Endpoint{endpoint})
	if err != nil {
		return err
	}
	enforcer := accesscontrol.NewEnforcer(registry, authorizer)
	_, path, _ := strings.Cut(endpoint.Operation, " ")
	chain := append([]gin.HandlerFunc{operationAuthorization(enforcer, endpoint, logger)}, handlers...)
	group.POST(strings.TrimPrefix(path, "/api/v1"), chain...)
	return nil
}

func operationAuthorization(enforcer *accesscontrol.Enforcer, endpoint accesscontrol.Endpoint, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if endpoint.Authentication == accesscontrol.AuthenticationJWT && !strings.HasPrefix(strings.ToLower(c.GetHeader("Authorization")), "bearer ") {
			Fail(c, logger, apperror.Unauthorized("bearer token is required"))
			return
		}
		resolved, err := enforcer.Authorize(c.Request.Context(), accesscontrol.TransportHTTP, endpoint.Operation)
		if err != nil {
			switch {
			case errors.Is(err, accesscontrol.ErrEndpointMissing):
				Fail(c, logger, apperror.PermissionPolicyMissing(err))
			case errors.Is(err, accesscontrol.ErrAuthentication):
				Fail(c, logger, apperror.Unauthorized("authentication is required"))
			case errors.Is(err, platformauthz.ErrDecisionUnavailable):
				Fail(c, logger, apperror.AuthorizationUnavailable(err))
			default:
				Fail(c, logger, apperror.Forbidden("permission denied"))
			}
			return
		}
		c.Set("authorization_endpoint", resolved)
		c.Request = c.Request.WithContext(accesscontrol.WithEndpoint(c.Request.Context(), resolved))
		c.Next()
	}
}
