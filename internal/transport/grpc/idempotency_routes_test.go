package grpctransport

import (
	"strings"
	"testing"

	identityv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/identity/v1"
	"google.golang.org/grpc"
)

func TestValidateGRPCIdempotencyMethods(t *testing.T) {
	t.Parallel()
	services := map[string]grpc.ServiceInfo{
		"orders.v1.OrderService": {Methods: []grpc.MethodInfo{
			{Name: "Create"}, {Name: "List"}, {Name: "Watch", IsServerStream: true},
		}},
		"grpc.health.v1.Health":                {Methods: []grpc.MethodInfo{{Name: "Check"}}},
		"platform.identity.v1.IdentityService": {Methods: []grpc.MethodInfo{{Name: "IssueTenantToken"}}},
	}
	approved := map[string]struct{}{"/orders.v1.OrderService/Create": {}}
	for _, test := range []struct {
		name     string
		patterns []string
		want     string
	}{
		{name: "no selected method"},
		{name: "approved mutation", patterns: []string{"/orders.v1.OrderService/Create"}},
		{name: "unapproved read", patterns: []string{"/orders.v1.OrderService/List"}, want: "without code-owned idempotency approval"},
		{name: "wildcard includes unapproved methods", patterns: []string{"/orders.v1.OrderService/*"}, want: "idempotency.grpc_methods pattern"},
		{name: "streaming method", patterns: []string{"/orders.v1.OrderService/Watch"}, want: "streaming RPC"},
		{name: "infrastructure method", patterns: []string{"/grpc.health.v1.Health/Check"}, want: "infrastructure RPC"},
		{name: "token response", patterns: []string{"/platform.identity.v1.IdentityService/IssueTenantToken"}, want: "without code-owned idempotency approval"},
		{name: "unknown method", patterns: []string{"/orders.v1.OrderService/Delete"}, want: "matches no registered RPC"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateGRPCIdempotencyMethods(test.patterns, services, approved)
			if test.want == "" && err != nil {
				t.Fatalf("validateGRPCIdempotencyMethods() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("validateGRPCIdempotencyMethods() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCurrentIdentityRPCsAreNotIdempotencyApproved(t *testing.T) {
	t.Parallel()
	server := grpc.NewServer()
	identityv1.RegisterIdentityServiceServer(server, &identityServer{})
	if err := validateGRPCIdempotencyMethods(
		[]string{identityv1.IdentityService_IssueTenantToken_FullMethodName},
		server.GetServiceInfo(), eligibleGRPCIdempotencyMethods,
	); err == nil || !strings.Contains(err.Error(), "without code-owned idempotency approval") {
		t.Fatalf("sensitive identity RPC must not be replayable: %v", err)
	}
}
