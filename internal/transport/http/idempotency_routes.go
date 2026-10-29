package httptransport

import (
	"fmt"
	"strings"

	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/auth"
)

// idempotencyEligibleHTTPRoutes is a code-owned response-safety review. A
// deployment can select a subset through configuration, but config wildcards
// cannot opt a newly added route in without an explicit code change.
var idempotencyEligibleHTTPRoutes = map[string]struct{}{
	"/api/v1/auth/password/reset":                              {},
	"/api/v1/auth/password/change":                             {},
	"/api/v1/auth/sessions/revoke":                             {},
	"/api/v1/auth/sessions/logout-all":                         {},
	"/api/v1/auth/sessions/force-logout-all":                   {},
	"/api/v1/files/delete":                                     {},
	"/api/v1/users/create":                                     {},
	"/api/v1/users/update":                                     {},
	"/api/v1/users/delete":                                     {},
	"/api/v1/service-accounts/update":                          {},
	"/api/v1/service-accounts/delete":                          {},
	"/api/v1/platform/tenants/create":                          {},
	"/api/v1/platform/tenants/update":                          {},
	"/api/v1/platform/tenants/delete":                          {},
	"/api/v1/tenants/update":                                   {},
	"/api/v1/tenants/delete":                                   {},
	"/api/v1/tenant-members/add":                               {},
	"/api/v1/tenant-members/status/update":                     {},
	"/api/v1/tenant-members/remove":                            {},
	"/api/v1/tenant-departments/create":                        {},
	"/api/v1/tenant-departments/update":                        {},
	"/api/v1/tenant-departments/delete":                        {},
	"/api/v1/tenant-departments/members/set":                   {},
	"/api/v1/platform/tenant-authorization/permissions/set":    {},
	"/api/v1/platform/tenant-authorization/administrators/set": {},
	"/api/v1/tenant-authorization/administrators/set":          {},
	"/api/v1/tenant-roles/create":                              {},
	"/api/v1/tenant-roles/update":                              {},
	"/api/v1/tenant-roles/delete":                              {},
	"/api/v1/tenant-roles/permissions/set":                     {},
	"/api/v1/tenant-members/roles/set":                         {},
	"/api/v1/applications/create":                              {},
	"/api/v1/applications/update":                              {},
	"/api/v1/applications/delete":                              {},
	"/api/v1/platform/tenant-applications/grant":               {},
	"/api/v1/platform/tenant-applications/revoke":              {},
	"/api/v1/navigations/create":                               {},
	"/api/v1/navigations/update":                               {},
	"/api/v1/navigations/delete":                               {},
	"/api/v1/permissions/create":                               {},
	"/api/v1/permissions/update":                               {},
	"/api/v1/permissions/delete":                               {},
	"/api/v1/operation-logs/frontend/record":                   {},
	"/api/v1/dictionaries/create":                              {},
	"/api/v1/dictionaries/update":                              {},
	"/api/v1/dictionaries/delete":                              {},
	"/api/v1/dictionary-items/create":                          {},
	"/api/v1/dictionary-items/update":                          {},
	"/api/v1/dictionary-items/delete":                          {},
	"/api/v1/scheduled-jobs/trigger":                           {},
}

// validateHTTPIdempotencyRoutes checks the effective runtime route table, not
// just the paths known when config validation was written. A wildcard must not
// start caching a newly registered query or anonymous response by accident.
func validateHTTPIdempotencyRoutes(patterns []string, endpoints []accesscontrol.Endpoint) error {
	for _, pattern := range patterns {
		matched := false
		for _, endpoint := range endpoints {
			if endpoint.Transport != accesscontrol.TransportHTTP {
				continue
			}
			_, route, ok := strings.Cut(endpoint.Operation, " ")
			if !ok || !auth.MatchesAny(route, []string{pattern}) {
				continue
			}
			matched = true
			if endpoint.Authentication == accesscontrol.AuthenticationPublic {
				return fmt.Errorf("idempotency.http_paths pattern %q includes public route %q", pattern, route)
			}
			if idempotencyQueryAction(endpoint.Action) || idempotencyQueryRoute(route) {
				return fmt.Errorf("idempotency.http_paths pattern %q includes query route %q", pattern, route)
			}
			if _, eligible := idempotencyEligibleHTTPRoutes[route]; !eligible {
				return fmt.Errorf("idempotency.http_paths pattern %q includes route %q without code-owned idempotency approval", pattern, route)
			}
		}
		if !matched {
			return fmt.Errorf("idempotency.http_paths pattern %q matches no registered HTTP route", pattern)
		}
	}
	return nil
}

func idempotencyQueryAction(action string) bool {
	switch action {
	case "read", "list", "evaluate", "simulate", "download":
		return true
	default:
		return false
	}
}

func idempotencyQueryRoute(route string) bool {
	for _, suffix := range []string{
		"/get", "/page", "/list", "/tree", "/query", "/evaluate",
		"/simulate", "/download", "/target", "-target", "/current", "/available",
	} {
		if strings.HasSuffix(route, suffix) {
			return true
		}
	}
	return false
}
