package grpctransport

import (
	"fmt"

	"github.com/lihongjie0209/go-api-template/internal/auth"
	"google.golang.org/grpc"
)

// eligibleGRPCIdempotencyMethods is a code-owned response-safety approval.
// The currently registered business RPCs are reads or authentication methods;
// none is approved for durable response replay yet. New mutation RPCs must be
// reviewed and explicitly added here before config may select them.
var eligibleGRPCIdempotencyMethods = map[string]struct{}{}

func validateGRPCIdempotencyMethods(patterns []string, services map[string]grpc.ServiceInfo, eligible map[string]struct{}) error {
	for _, pattern := range patterns {
		matched := false
		for serviceName, service := range services {
			for _, method := range service.Methods {
				operation := "/" + serviceName + "/" + method.Name
				if !auth.MatchesAny(operation, []string{pattern}) {
					continue
				}
				matched = true
				if isInfrastructureMethod(operation) {
					return fmt.Errorf("idempotency.grpc_methods pattern %q includes infrastructure RPC %q", pattern, operation)
				}
				if method.IsClientStream || method.IsServerStream {
					return fmt.Errorf("idempotency.grpc_methods pattern %q includes streaming RPC %q", pattern, operation)
				}
				if _, approved := eligible[operation]; !approved {
					return fmt.Errorf("idempotency.grpc_methods pattern %q includes RPC %q without code-owned idempotency approval", pattern, operation)
				}
			}
		}
		if !matched {
			return fmt.Errorf("idempotency.grpc_methods pattern %q matches no registered RPC", pattern)
		}
	}
	return nil
}
