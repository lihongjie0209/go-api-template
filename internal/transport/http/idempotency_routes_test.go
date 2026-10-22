package httptransport

import (
	"strings"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
)

func TestValidateHTTPIdempotencyRoutes(t *testing.T) {
	t.Parallel()
	endpoints := []accesscontrol.Endpoint{
		{Transport: accesscontrol.TransportHTTP, Operation: "POST /api/v1/users/create", Authentication: accesscontrol.AuthenticationJWT, Action: "create"},
		{Transport: accesscontrol.TransportHTTP, Operation: "POST /api/v1/users/page", Authentication: accesscontrol.AuthenticationJWT, Action: "list"},
		{Transport: accesscontrol.TransportHTTP, Operation: "POST /api/v1/users/update-target", Authentication: accesscontrol.AuthenticationJWT, Action: "update"},
		{Transport: accesscontrol.TransportHTTP, Operation: "POST /api/v1/auth/login", Authentication: accesscontrol.AuthenticationPublic},
		{Transport: accesscontrol.TransportHTTP, Operation: "POST /api/v1/auth/logout", Authentication: accesscontrol.AuthenticationPublic},
		{Transport: accesscontrol.TransportHTTP, Operation: "POST /api/v1/authorization/rows/evaluate", Authentication: accesscontrol.AuthenticationJWT, Action: "evaluate"},
		{Transport: accesscontrol.TransportGRPC, Operation: "/identity.v1.IdentityService/Create", Action: "create"},
	}
	for _, test := range []struct {
		name     string
		patterns []string
		want     string
	}{
		{name: "exact mutation", patterns: []string{"/api/v1/users/create"}},
		{name: "anonymous logout", patterns: []string{"/api/v1/auth/logout"}},
		{name: "wildcard includes query", patterns: []string{"/api/v1/users/*"}, want: "query route"},
		{name: "query action", patterns: []string{"/api/v1/authorization/rows/evaluate"}, want: "query route"},
		{name: "target is not a mutation", patterns: []string{"/api/v1/users/update-target"}, want: "query route"},
		{name: "anonymous login", patterns: []string{"/api/v1/auth/login"}, want: "public route"},
		{name: "unknown route", patterns: []string{"/api/v1/unknown/create"}, want: "matches no registered HTTP route"},
		{name: "grpc operation is not an HTTP route", patterns: []string{"/identity.v1.IdentityService/Create"}, want: "matches no registered HTTP route"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateHTTPIdempotencyRoutes(test.patterns, endpoints)
			if test.want == "" && err != nil {
				t.Fatalf("validateHTTPIdempotencyRoutes() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("validateHTTPIdempotencyRoutes() error = %v, want %q", err, test.want)
			}
		})
	}
}
