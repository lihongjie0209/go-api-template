package authorization

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrCapabilityInvalid     = errors.New("authorization capability request is invalid")
	ErrCapabilityUnavailable = errors.New("authorization capability evaluation is unavailable")
)

const (
	MaxCapabilityItems   = 100
	MaxCapabilityRows    = 200
	MaxCapabilityActions = 20
)

type CapabilityRequest struct {
	Key      string `json:"key"`
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

type CapabilityDecision struct {
	Key     string `json:"key"`
	Allowed bool   `json:"allowed"`
}

type CapabilityResult struct {
	Revision  string               `json:"revision"`
	ExpiresAt time.Time            `json:"expires_at"`
	Items     []CapabilityDecision `json:"items"`
}

type RowCapabilityDecision struct {
	ResourceID string          `json:"resource_id"`
	Actions    map[string]bool `json:"actions"`
}

type RowCapabilityResult struct {
	Revision  string                  `json:"revision"`
	ExpiresAt time.Time               `json:"expires_at"`
	Resource  string                  `json:"resource"`
	Items     []RowCapabilityDecision `json:"items"`
}

// CapabilityService exposes decisions only for the authenticated principal.
// It never accepts subject attributes or resource attributes from the client.
type CapabilityService struct {
	authorizer platformauthz.Authorizer
	dataScopes *datapermission.Service
	resources  *pbac.Registry
	providers  *RowCapabilityRegistry
	revisions  policyRevisions
	now        func() time.Time
	endpoints  atomic.Pointer[accesscontrol.EndpointRegistry]
}

type policyRevisions interface {
	Revision() string
}

func NewCapabilityService(authorizer platformauthz.Authorizer, dataScopes *datapermission.Service, resources *pbac.Registry, providers *RowCapabilityRegistry, revisions *PolicyRevisions) *CapabilityService {
	return newCapabilityService(authorizer, dataScopes, resources, providers, revisions, time.Now)
}

func newCapabilityService(authorizer platformauthz.Authorizer, dataScopes *datapermission.Service, resources *pbac.Registry, providers *RowCapabilityRegistry, revisions policyRevisions, now func() time.Time) *CapabilityService {
	return &CapabilityService{authorizer: authorizer, dataScopes: dataScopes, resources: resources, providers: providers, revisions: revisions, now: now}
}

// SetEndpointRegistry publishes the immutable runtime endpoint catalog before
// the HTTP listener starts accepting requests.
func (s *CapabilityService) SetEndpointRegistry(registry *accesscontrol.EndpointRegistry) {
	if s != nil && registry != nil {
		s.endpoints.Store(registry)
	}
}

func (s *CapabilityService) Evaluate(ctx context.Context, requests []CapabilityRequest) (CapabilityResult, error) {
	if s == nil || s.authorizer == nil || s.resources == nil || len(requests) == 0 || len(requests) > MaxCapabilityItems {
		return CapabilityResult{}, ErrCapabilityInvalid
	}
	principal, err := platformprincipal.Require(ctx)
	if err != nil {
		return CapabilityResult{}, err
	}
	ctx = withAuthorizationMemo(ctx)
	result := s.resultMetadata()
	result.Items = make([]CapabilityDecision, len(requests))
	seen := make(map[string]struct{}, len(requests))
	for index, request := range requests {
		if err := validateCapabilityRequest(request, seen); err != nil {
			return CapabilityResult{}, err
		}
		allowed, err := s.evaluateOperation(ctx, principal, request.Resource, request.Action)
		if err != nil {
			return CapabilityResult{}, err
		}
		result.Items[index] = CapabilityDecision{Key: request.Key, Allowed: allowed}
	}
	return result, nil
}

func (s *CapabilityService) EvaluateRows(ctx context.Context, resource string, actions, resourceIDs []string) (RowCapabilityResult, error) {
	if s == nil || s.authorizer == nil || s.resources == nil || s.dataScopes == nil || s.providers == nil || resource != strings.TrimSpace(resource) || resource == "" || len(actions) == 0 || len(actions) > MaxCapabilityActions || len(resourceIDs) == 0 || len(resourceIDs) > MaxCapabilityRows {
		return RowCapabilityResult{}, ErrCapabilityInvalid
	}
	principal, err := platformprincipal.Require(ctx)
	if err != nil {
		return RowCapabilityResult{}, err
	}
	ctx = withAuthorizationMemo(ctx)
	actions, err = normalizeCapabilityValues(actions, MaxCapabilityActions)
	if err != nil {
		return RowCapabilityResult{}, err
	}
	resourceIDs, err = normalizeCapabilityValues(resourceIDs, MaxCapabilityRows)
	if err != nil {
		return RowCapabilityResult{}, err
	}
	definition, ok := s.resources.Resource(resource)
	if !ok || definition.Scope != pbac.ResourceScopeTenant || principal.TenantID == "" {
		return RowCapabilityResult{}, ErrCapabilityInvalid
	}
	provider, ok := s.providers.Provider(resource)
	if !ok {
		return RowCapabilityResult{}, ErrCapabilityInvalid
	}
	for _, action := range actions {
		if _, _, err := s.resources.Resolve(resource, action); err != nil {
			return RowCapabilityResult{}, ErrCapabilityInvalid
		}
	}
	metadata := s.resultMetadata()
	result := RowCapabilityResult{Revision: metadata.Revision, ExpiresAt: metadata.ExpiresAt, Resource: resource, Items: make([]RowCapabilityDecision, len(resourceIDs))}
	for index, id := range resourceIDs {
		result.Items[index] = RowCapabilityDecision{ResourceID: id, Actions: make(map[string]bool, len(actions))}
		for _, action := range actions {
			result.Items[index].Actions[action] = false
		}
	}
	allowedActions := make([]string, 0, len(actions))
	for _, action := range actions {
		operationAllowed, err := s.evaluateOperation(ctx, principal, resource, action)
		if err != nil {
			return RowCapabilityResult{}, err
		}
		if !operationAllowed {
			continue
		}
		allowedActions = append(allowedActions, action)
	}
	if len(allowedActions) == 0 {
		return result, nil
	}
	rows, err := provider.Load(ctx, principal.TenantID, resourceIDs)
	if err != nil {
		return RowCapabilityResult{}, err
	}
	if err := validateCapabilityRows(rows, resourceIDs); err != nil {
		return RowCapabilityResult{}, err
	}
	attributes := make([]datapermission.ResourceAttributes, 0, len(resourceIDs))
	positions := make([]int, 0, len(resourceIDs))
	for index, id := range resourceIDs {
		if row, exists := rows[id]; exists {
			attributes = append(attributes, row)
			positions = append(positions, index)
		}
	}
	if len(attributes) == 0 {
		return result, nil
	}
	decisions, err := s.dataScopes.EvaluateRowActions(ctx, resource, allowedActions, attributes)
	if err != nil {
		return RowCapabilityResult{}, fmt.Errorf("%w: %v", ErrCapabilityUnavailable, err)
	}
	for _, action := range allowedActions {
		for index, value := range decisions[action] {
			result.Items[positions[index]].Actions[action] = value
		}
	}
	return result, nil
}

func (s *CapabilityService) resultMetadata() CapabilityResult {
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	revision := ""
	if s != nil && s.revisions != nil {
		revision = s.revisions.Revision()
	}
	return CapabilityResult{Revision: revision, ExpiresAt: now().In(operationLocation).Add(30 * time.Second)}
}

func validateCapabilityRows(rows map[string]datapermission.ResourceAttributes, requestedIDs []string) error {
	requested := make(map[string]struct{}, len(requestedIDs))
	for _, id := range requestedIDs {
		requested[id] = struct{}{}
	}
	for id, attributes := range rows {
		if _, ok := requested[id]; !ok {
			return ErrCapabilityUnavailable
		}
		attributeID, ok := attributes["id"].(string)
		if !ok || attributeID != id {
			return ErrCapabilityUnavailable
		}
	}
	return nil
}

func (s *CapabilityService) evaluateOperation(ctx context.Context, principal platformprincipal.Principal, resource, action string) (bool, error) {
	definition, _, err := s.resources.Resolve(resource, action)
	if err != nil {
		return false, ErrCapabilityInvalid
	}
	registry := s.endpoints.Load()
	if registry == nil {
		return false, ErrCapabilityUnavailable
	}
	targets := registry.Find(resource, action)
	if len(targets) == 0 {
		return false, ErrCapabilityInvalid
	}
	scope := platformauthz.ScopeTenant
	switch definition.Scope {
	case pbac.ResourceScopePlatform:
		scope = platformauthz.ScopePlatform
	case pbac.ResourceScopePrincipal:
		scope = platformauthz.ScopePrincipal
	}
	denied := false
	for _, endpoint := range targets {
		if endpoint.Transport != accesscontrol.TransportHTTP || endpoint.Authentication != accesscontrol.AuthenticationJWT {
			continue
		}
		targetContext := accesscontrol.WithEndpoint(ctx, endpoint)
		err := s.authorizer.Authorize(targetContext, principal, platformauthz.Requirement{Resource: resource, Action: action, Scope: scope})
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, platformauthz.ErrDenied):
			denied = true
		default:
			return false, fmt.Errorf("%w: %v", ErrCapabilityUnavailable, err)
		}
	}
	if denied {
		return false, nil
	}
	return false, ErrCapabilityInvalid
}

func validateCapabilityRequest(request CapabilityRequest, seen map[string]struct{}) error {
	if request.Key != strings.TrimSpace(request.Key) || request.Resource != strings.TrimSpace(request.Resource) || request.Action != strings.TrimSpace(request.Action) || request.Key == "" || request.Resource == "" || request.Action == "" || len(request.Key) > 128 {
		return ErrCapabilityInvalid
	}
	if _, exists := seen[request.Key]; exists {
		return ErrCapabilityInvalid
	}
	seen[request.Key] = struct{}{}
	return nil
}

func normalizeCapabilityValues(values []string, maximum int) ([]string, error) {
	if len(values) == 0 || len(values) > maximum {
		return nil, ErrCapabilityInvalid
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 256 {
			return nil, ErrCapabilityInvalid
		}
		if _, exists := seen[value]; exists {
			return nil, ErrCapabilityInvalid
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}
