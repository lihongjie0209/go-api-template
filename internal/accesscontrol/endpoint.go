package accesscontrol

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lihongjie0209/go-api-template/internal/pbac"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrInvalidEndpoint   = errors.New("access control: invalid endpoint authorization")
	ErrDuplicateEndpoint = errors.New("access control: duplicate endpoint authorization")
	ErrEndpointCoverage  = errors.New("access control: endpoint authorization coverage mismatch")
	ErrEndpointMissing   = errors.New("access control: endpoint authorization is missing")
	ErrAuthentication    = errors.New("access control: authentication requirement is not satisfied")
)

// Transport identifies the operation namespace.
type Transport string

const (
	TransportHTTP Transport = "http"
	TransportGRPC Transport = "grpc"
)

// AuthenticationMode declares how an operation obtains a trusted principal.
type AuthenticationMode string

const (
	AuthenticationPublic  AuthenticationMode = "public"
	AuthenticationJWT     AuthenticationMode = "jwt"
	AuthenticationPSK     AuthenticationMode = "psk"
	AuthenticationService AuthenticationMode = "service"
)

// DataPermissionMode declares the Service-layer data-scope obligation.
type DataPermissionMode string

const (
	DataPermissionNone     DataPermissionMode = "none"
	DataPermissionRequired DataPermissionMode = "required"
	DataPermissionObject   DataPermissionMode = "object"
)

// Endpoint binds one transport operation to its complete authorization metadata.
type Endpoint struct {
	Transport      Transport          `json:"transport"`
	Operation      string             `json:"operation"`
	Authentication AuthenticationMode `json:"authentication"`
	Resource       string             `json:"resource,omitempty"`
	Action         string             `json:"action,omitempty"`
	Scope          pbac.ResourceScope `json:"scope,omitempty"`
	DataPermission DataPermissionMode `json:"data_permission"`
	// DataPermissionReason is mandatory for protected operations that
	// deliberately do not use row-level data permission.
	DataPermissionReason string `json:"data_permission_reason,omitempty"`
}

// Operation identifies one route or RPC discovered from the running server.
type Operation struct {
	Transport Transport
	Name      string
}

type endpointContextKey struct{}

// WithEndpoint carries the already-authorized descriptor into application and
// repository layers so required data permission cannot be inferred from paths.
func WithEndpoint(ctx context.Context, endpoint Endpoint) context.Context {
	return context.WithValue(ctx, endpointContextKey{}, endpoint)
}

// EndpointFromContext returns the descriptor selected by the transport.
func EndpointFromContext(ctx context.Context) (Endpoint, bool) {
	if ctx == nil {
		return Endpoint{}, false
	}
	endpoint, ok := ctx.Value(endpointContextKey{}).(Endpoint)
	return endpoint, ok
}

// EndpointRegistry is the immutable source of transport authorization metadata.
type EndpointRegistry struct {
	endpoints map[string]Endpoint
}

// NewEndpointRegistry validates descriptors against the Resource/Action registry.
func NewEndpointRegistry(resources *pbac.Registry, endpoints []Endpoint) (*EndpointRegistry, error) {
	if resources == nil {
		return nil, ErrInvalidEndpoint
	}
	registry := &EndpointRegistry{endpoints: make(map[string]Endpoint, len(endpoints))}
	for _, endpoint := range endpoints {
		definition, err := validateEndpoint(resources, endpoint)
		if err != nil {
			return nil, err
		}
		endpoint.Scope = definition.Scope
		key := endpointKey(endpoint.Transport, endpoint.Operation)
		if _, exists := registry.endpoints[key]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateEndpoint, endpoint.Operation)
		}
		registry.endpoints[key] = endpoint
	}
	return registry, nil
}

// Lookup returns a copy of one operation descriptor.
func (r *EndpointRegistry) Lookup(transport Transport, operation string) (Endpoint, bool) {
	if r == nil {
		return Endpoint{}, false
	}
	endpoint, ok := r.endpoints[endpointKey(transport, operation)]
	return endpoint, ok
}

// Definitions returns descriptors in stable transport/operation order.
func (r *EndpointRegistry) Definitions() []Endpoint {
	if r == nil {
		return nil
	}
	result := make([]Endpoint, 0, len(r.endpoints))
	for _, endpoint := range r.endpoints {
		result = append(result, endpoint)
	}
	slices.SortFunc(result, func(left, right Endpoint) int {
		return strings.Compare(endpointKey(left.Transport, left.Operation), endpointKey(right.Transport, right.Operation))
	})
	return result
}

// ValidateCoverage requires an exact match between runtime operations and descriptors.
func (r *EndpointRegistry) ValidateCoverage(operations []Operation) error {
	if r == nil {
		return ErrEndpointCoverage
	}
	actual := make(map[string]struct{}, len(operations))
	for _, operation := range operations {
		if !validOperation(operation.Transport, operation.Name) {
			return fmt.Errorf("%w: invalid runtime operation", ErrEndpointCoverage)
		}
		key := endpointKey(operation.Transport, operation.Name)
		if _, duplicate := actual[key]; duplicate {
			return fmt.Errorf("%w: duplicate runtime operation %s", ErrEndpointCoverage, operation.Name)
		}
		actual[key] = struct{}{}
	}
	if len(actual) != len(r.endpoints) {
		return fmt.Errorf("%w: runtime=%d declared=%d", ErrEndpointCoverage, len(actual), len(r.endpoints))
	}
	for key := range actual {
		if _, declared := r.endpoints[key]; !declared {
			return fmt.Errorf("%w: runtime operation is undeclared", ErrEndpointCoverage)
		}
	}
	return nil
}

func validateEndpoint(resources *pbac.Registry, endpoint Endpoint) (pbac.ResourceDefinition, error) {
	if !validOperation(endpoint.Transport, endpoint.Operation) || !validAuthentication(endpoint.Authentication) || !validDataPermission(endpoint.DataPermission) {
		return pbac.ResourceDefinition{}, ErrInvalidEndpoint
	}
	if endpoint.Scope != "" {
		return pbac.ResourceDefinition{}, fmt.Errorf("%w: scope is derived from the resource", ErrInvalidEndpoint)
	}
	if endpoint.Authentication == AuthenticationPublic {
		if endpoint.Resource != "" || endpoint.Action != "" || endpoint.DataPermission != DataPermissionNone || endpoint.DataPermissionReason != "" {
			return pbac.ResourceDefinition{}, fmt.Errorf("%w: public operation cannot declare business authorization", ErrInvalidEndpoint)
		}
		return pbac.ResourceDefinition{}, nil
	}
	if endpoint.Resource == "" || endpoint.Action == "" {
		return pbac.ResourceDefinition{}, fmt.Errorf("%w: protected operation requires resource and action", ErrInvalidEndpoint)
	}
	definition, _, err := resources.Resolve(endpoint.Resource, endpoint.Action)
	if err != nil {
		return pbac.ResourceDefinition{}, err
	}
	if endpoint.DataPermission == DataPermissionNone {
		if endpoint.DataPermissionReason == "" || endpoint.DataPermissionReason != strings.TrimSpace(endpoint.DataPermissionReason) || len(endpoint.DataPermissionReason) > 256 {
			return pbac.ResourceDefinition{}, fmt.Errorf("%w: protected operation without data permission requires a bounded justification", ErrInvalidEndpoint)
		}
	} else if endpoint.DataPermissionReason != "" {
		return pbac.ResourceDefinition{}, fmt.Errorf("%w: data-permission enforcement cannot declare an exemption", ErrInvalidEndpoint)
	}
	return definition, nil
}

// Enforcer performs the transport-independent operation authorization check.
type Enforcer struct {
	registry   *EndpointRegistry
	authorizer platformauthz.Authorizer
}

func NewEnforcer(registry *EndpointRegistry, authorizer platformauthz.Authorizer) *Enforcer {
	return &Enforcer{registry: registry, authorizer: authorizer}
}

// Authorize fails closed for undeclared operations and missing principals.
func (e *Enforcer) Authorize(ctx context.Context, transport Transport, operation string) (Endpoint, error) {
	if e == nil || e.registry == nil {
		return Endpoint{}, ErrEndpointMissing
	}
	endpoint, ok := e.registry.Lookup(transport, operation)
	if !ok {
		return Endpoint{}, fmt.Errorf("%w: %s", ErrEndpointMissing, operation)
	}
	if endpoint.Authentication == AuthenticationPublic {
		return endpoint, nil
	}
	principal, ok := platformprincipal.FromContext(ctx)
	if !ok {
		return Endpoint{}, ErrAuthentication
	}
	if endpoint.Authentication == AuthenticationService && principal.Type != platformprincipal.TypeServiceAccount && principal.Type != platformprincipal.TypeSystem {
		return Endpoint{}, ErrAuthentication
	}
	scope := platformauthz.ScopeTenant
	switch endpoint.Scope {
	case pbac.ResourceScopePlatform:
		scope = platformauthz.ScopePlatform
	case pbac.ResourceScopePrincipal:
		scope = platformauthz.ScopePrincipal
	}
	err := platformauthz.Enforce(ctx, e.authorizer, platformauthz.Requirement{Resource: endpoint.Resource, Action: endpoint.Action, Scope: scope})
	return endpoint, err
}

func validAuthentication(mode AuthenticationMode) bool {
	return mode == AuthenticationPublic || mode == AuthenticationJWT || mode == AuthenticationPSK || mode == AuthenticationService
}

func validDataPermission(mode DataPermissionMode) bool {
	return mode == DataPermissionNone || mode == DataPermissionRequired || mode == DataPermissionObject
}

func validOperation(transport Transport, operation string) bool {
	if operation != strings.TrimSpace(operation) || operation == "" {
		return false
	}
	switch transport {
	case TransportHTTP:
		method, path, ok := strings.Cut(operation, " ")
		return ok && method != "" && method == strings.ToUpper(method) && strings.HasPrefix(path, "/") && !strings.Contains(path, " ")
	case TransportGRPC:
		return strings.HasPrefix(operation, "/") && strings.Count(operation, "/") == 2 && !strings.Contains(operation, " ")
	default:
		return false
	}
}

func endpointKey(transport Transport, operation string) string {
	return string(transport) + "\x00" + operation
}
