package pbac

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const (
	MaxResourceKeyLength = 128
	MaxActionKeyLength   = 64
)

var (
	ErrInvalidResourceAction = errors.New("pbac: invalid resource action")
	ErrDuplicateResource     = errors.New("pbac: duplicate resource")
	ErrDuplicateAction       = errors.New("pbac: duplicate action")
	ErrUnknownResource       = errors.New("pbac: unknown resource")
	ErrUnknownAction         = errors.New("pbac: unknown action")

	resourceKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)*$`)
	actionKeyPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// ResourceScope identifies whether a resource is controlled by the platform
// or may be authorized by tenant-owned policies.
type ResourceScope string

const (
	ResourceScopePlatform ResourceScope = "platform"
	ResourceScopeTenant   ResourceScope = "tenant"
	// ResourceScopePrincipal identifies operations constrained to the
	// authenticated subject by the owning service. Policies remain global;
	// tenant-owned policies cannot grant principal or platform resources.
	ResourceScopePrincipal ResourceScope = "principal"
)

// ResourceAction is the canonical <resource>:<action> authorization name.
type ResourceAction struct {
	Resource string `json:"resource" yaml:"resource"`
	Action   string `json:"action" yaml:"action"`
}

// ParseResourceAction parses a canonical <resource>:<action> name. Resource
// segments use dots; the single colon is reserved as the action separator.
func ParseResourceAction(value string) (ResourceAction, error) {
	if value != strings.TrimSpace(value) || strings.Count(value, ":") != 1 {
		return ResourceAction{}, fmt.Errorf("%w: expected <resource>:<action>", ErrInvalidResourceAction)
	}
	resource, action, _ := strings.Cut(value, ":")
	result := ResourceAction{Resource: resource, Action: action}
	if err := result.Validate(); err != nil {
		return ResourceAction{}, err
	}
	return result, nil
}

// Validate checks the canonical resource and action naming contract.
func (r ResourceAction) Validate() error {
	if !validResourceKey(r.Resource) {
		return fmt.Errorf("%w: invalid resource %q", ErrInvalidResourceAction, r.Resource)
	}
	if !validActionKey(r.Action) {
		return fmt.Errorf("%w: invalid action %q", ErrInvalidResourceAction, r.Action)
	}
	return nil
}

func (r ResourceAction) String() string { return r.Resource + ":" + r.Action }

// ActionDefinition describes one action supported by a resource.
type ActionDefinition struct {
	Key         string `json:"key" yaml:"key"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// ResourceDefinition is registered by the business module that owns the
// resource. Keys are authorization contracts and must not be HTTP paths or UI
// labels.
type ResourceDefinition struct {
	Key         string             `json:"key" yaml:"key"`
	Name        string             `json:"name" yaml:"name"`
	Description string             `json:"description,omitempty" yaml:"description,omitempty"`
	Scope       ResourceScope      `json:"scope" yaml:"scope"`
	Actions     []ActionDefinition `json:"actions" yaml:"actions"`
}

// ResourceDefinitions is the Fx value-group unit contributed by one business
// module. The application flattens all contributions before validation.
type ResourceDefinitions []ResourceDefinition

// NewRegistryFromGroups builds the single immutable application registry.
func NewRegistryFromGroups(groups []ResourceDefinitions) (*Registry, error) {
	definitions := make([]ResourceDefinition, 0)
	for _, group := range groups {
		definitions = append(definitions, group...)
	}
	return NewRegistry(definitions)
}

func NewRegistryFromDefinitions(definitions ResourceDefinitions) (*Registry, error) {
	return NewRegistry(definitions)
}

// Registry is an immutable index of all resource-action names registered by
// business modules during application construction.
type Registry struct {
	resources map[string]ResourceDefinition
	actions   map[string]ActionDefinition
}

// NewRegistry validates all definitions and creates an immutable registry.
func NewRegistry(definitions []ResourceDefinition) (*Registry, error) {
	registry := &Registry{
		resources: make(map[string]ResourceDefinition, len(definitions)),
		actions:   make(map[string]ActionDefinition),
	}
	for _, definition := range definitions {
		if err := registry.add(definition); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *Registry) add(definition ResourceDefinition) error {
	if !validResourceKey(definition.Key) || strings.TrimSpace(definition.Name) == "" {
		return fmt.Errorf("%w: invalid resource definition %q", ErrInvalidResourceAction, definition.Key)
	}
	if definition.Scope != ResourceScopePlatform && definition.Scope != ResourceScopeTenant && definition.Scope != ResourceScopePrincipal {
		return fmt.Errorf("%w: invalid scope %q for resource %q", ErrInvalidResourceAction, definition.Scope, definition.Key)
	}
	if len(definition.Actions) == 0 {
		return fmt.Errorf("%w: resource %q has no actions", ErrInvalidResourceAction, definition.Key)
	}
	if _, exists := r.resources[definition.Key]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateResource, definition.Key)
	}

	definition.Name = strings.TrimSpace(definition.Name)
	definition.Description = strings.TrimSpace(definition.Description)
	definition.Actions = slices.Clone(definition.Actions)
	seen := make(map[string]struct{}, len(definition.Actions))
	for index := range definition.Actions {
		action := &definition.Actions[index]
		if !validActionKey(action.Key) || strings.TrimSpace(action.Name) == "" {
			return fmt.Errorf("%w: invalid action %q for resource %q", ErrInvalidResourceAction, action.Key, definition.Key)
		}
		if _, exists := seen[action.Key]; exists {
			return fmt.Errorf("%w: %s:%s", ErrDuplicateAction, definition.Key, action.Key)
		}
		seen[action.Key] = struct{}{}
		action.Name = strings.TrimSpace(action.Name)
		action.Description = strings.TrimSpace(action.Description)
		r.actions[ResourceAction{Resource: definition.Key, Action: action.Key}.String()] = *action
	}
	r.resources[definition.Key] = definition
	return nil
}

// Resource returns a defensive copy of a registered resource definition.
func (r *Registry) Resource(key string) (ResourceDefinition, bool) {
	if r == nil {
		return ResourceDefinition{}, false
	}
	definition, ok := r.resources[key]
	if !ok {
		return ResourceDefinition{}, false
	}
	definition.Actions = slices.Clone(definition.Actions)
	return definition, true
}

// Resolve verifies that a resource and action pair is registered.
func (r *Registry) Resolve(resource, action string) (ResourceDefinition, ActionDefinition, error) {
	definition, ok := r.Resource(resource)
	if !ok {
		return ResourceDefinition{}, ActionDefinition{}, fmt.Errorf("%w: %q", ErrUnknownResource, resource)
	}
	actionDefinition, ok := r.actions[ResourceAction{Resource: resource, Action: action}.String()]
	if !ok {
		return ResourceDefinition{}, ActionDefinition{}, fmt.Errorf("%w: %s:%s", ErrUnknownAction, resource, action)
	}
	return definition, actionDefinition, nil
}

// Definitions returns all registered resources in stable key order.
func (r *Registry) Definitions() []ResourceDefinition {
	if r == nil {
		return nil
	}
	definitions := make([]ResourceDefinition, 0, len(r.resources))
	for _, definition := range r.resources {
		definition.Actions = slices.Clone(definition.Actions)
		slices.SortFunc(definition.Actions, func(left, right ActionDefinition) int {
			return strings.Compare(left.Key, right.Key)
		})
		definitions = append(definitions, definition)
	}
	slices.SortFunc(definitions, func(left, right ResourceDefinition) int {
		return strings.Compare(left.Key, right.Key)
	})
	return definitions
}

func validResourceKey(value string) bool {
	return len(value) > 0 && len(value) <= MaxResourceKeyLength && resourceKeyPattern.MatchString(value)
}

func validActionKey(value string) bool {
	return len(value) > 0 && len(value) <= MaxActionKeyLength && actionKeyPattern.MatchString(value)
}
