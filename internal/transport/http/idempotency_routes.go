package httptransport

import (
	"fmt"
	"strings"

	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/auth"
)

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
			if endpoint.Authentication == accesscontrol.AuthenticationPublic && route != "/api/v1/auth/logout" {
				return fmt.Errorf("idempotency.http_paths pattern %q includes public route %q", pattern, route)
			}
			if idempotencyQueryAction(endpoint.Action) || idempotencyQueryRoute(route) {
				return fmt.Errorf("idempotency.http_paths pattern %q includes query route %q", pattern, route)
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
