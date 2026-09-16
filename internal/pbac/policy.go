package pbac

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	APIVersionV1          = "authorization.platform/v1"
	KindPolicy            = "ActionPolicy"
	MaxPolicyDocumentSize = 64 * 1024
	MaxPolicyActions      = 64
	MaxMatcherValues      = 128
)

var (
	ErrInvalidPolicy    = errors.New("pbac: invalid policy")
	policyCodePattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{2,127}$`)
	matcherValuePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,127}$`)
)

// PolicyScopeType controls whether a policy is global or belongs to one
// tenant. The tenant ID is supplied and enforced by the service boundary.
type PolicyScopeType string

const (
	PolicyScopeGlobal PolicyScopeType = "global"
	PolicyScopeTenant PolicyScopeType = "tenant"
)

// Effect is the result produced when all matchers and the condition match.
type Effect string

const (
	EffectAllow Effect = "allow"
	EffectDeny  Effect = "deny"
)

// Policy is the canonical PBAC v1 policy document.
type Policy struct {
	APIVersion string         `json:"api_version" yaml:"api_version"`
	Kind       string         `json:"kind" yaml:"kind"`
	Metadata   PolicyMetadata `json:"metadata" yaml:"metadata"`
	Scope      PolicyScope    `json:"scope" yaml:"scope"`
	Spec       PolicySpec     `json:"spec" yaml:"spec"`
}

type PolicyMetadata struct {
	Code        string `json:"code" yaml:"code"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

type PolicyScope struct {
	Type     PolicyScopeType `json:"type" yaml:"type"`
	TenantID string          `json:"tenant_id,omitempty" yaml:"tenant_id,omitempty"`
}

type PolicySpec struct {
	Subject  SubjectMatcher  `json:"subject" yaml:"subject"`
	Resource ResourceMatcher `json:"resource" yaml:"resource"`
	Actions  []string        `json:"actions" yaml:"actions"`
	Effect   Effect          `json:"effect" yaml:"effect"`
}

type SubjectMatcher struct {
	Authenticated *bool        `json:"authenticated,omitempty" yaml:"authenticated,omitempty"`
	Types         []string     `json:"types,omitempty" yaml:"types,omitempty"`
	IDs           []string     `json:"ids,omitempty" yaml:"ids,omitempty"`
	Role          string       `json:"-" yaml:"role,omitempty"`
	Roles         RolesMatcher `json:"roles,omitempty" yaml:"roles,omitempty"`
}

type RolesMatcher struct {
	AnyOf  []string `json:"any_of,omitempty" yaml:"any_of,omitempty"`
	AllOf  []string `json:"all_of,omitempty" yaml:"all_of,omitempty"`
	NoneOf []string `json:"none_of,omitempty" yaml:"none_of,omitempty"`
}

type ResourceMatcher struct {
	Type string `json:"type" yaml:"type"`
}

// ParsePolicy parses exactly one strict YAML document and normalizes accepted
// authoring conveniences into the canonical in-memory representation.
func ParsePolicy(data []byte) (Policy, error) {
	if len(data) == 0 || len(data) > MaxPolicyDocumentSize {
		return Policy{}, fmt.Errorf("%w: document size must be between 1 and %d bytes", ErrInvalidPolicy, MaxPolicyDocumentSize)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var policy Policy
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("%w: decode yaml: %v", ErrInvalidPolicy, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Policy{}, fmt.Errorf("%w: multiple yaml documents are not allowed", ErrInvalidPolicy)
		}
		return Policy{}, fmt.Errorf("%w: decode trailing yaml: %v", ErrInvalidPolicy, err)
	}
	policy.normalize()
	return policy, nil
}

// MarshalPolicy returns the canonical YAML representation persisted for an
// immutable policy version.
func MarshalPolicy(policy Policy) ([]byte, error) {
	policy.normalize()
	data, err := yaml.Marshal(policy)
	if err != nil {
		return nil, fmt.Errorf("marshal pbac policy: %w", err)
	}
	if len(data) > MaxPolicyDocumentSize {
		return nil, fmt.Errorf("%w: document exceeds %d bytes", ErrInvalidPolicy, MaxPolicyDocumentSize)
	}
	return data, nil
}

// Validate checks the complete publish-time structural contract against the
// immutable resource-action registry. CEL compilation is performed by the
// policy engine because it owns the typed attribute environment.
func (p Policy) Validate(registry *Registry) error {
	if registry == nil {
		return fmt.Errorf("%w: resource-action registry is required", ErrInvalidPolicy)
	}
	if p.APIVersion != APIVersionV1 || p.Kind != KindPolicy {
		return fmt.Errorf("%w: expected api_version %q and kind %q", ErrInvalidPolicy, APIVersionV1, KindPolicy)
	}
	if !policyCodePattern.MatchString(p.Metadata.Code) || strings.TrimSpace(p.Metadata.Name) == "" {
		return fmt.Errorf("%w: invalid metadata", ErrInvalidPolicy)
	}
	if p.Metadata.Name != strings.TrimSpace(p.Metadata.Name) || p.Metadata.Description != strings.TrimSpace(p.Metadata.Description) {
		return fmt.Errorf("%w: metadata values must be trimmed", ErrInvalidPolicy)
	}
	if err := validateScope(p.Scope); err != nil {
		return err
	}
	if err := validateSubject(p.Spec.Subject); err != nil {
		return err
	}
	if !validResourceKey(p.Spec.Resource.Type) {
		return fmt.Errorf("%w: invalid resource type %q", ErrInvalidPolicy, p.Spec.Resource.Type)
	}
	if len(p.Spec.Actions) == 0 || len(p.Spec.Actions) > MaxPolicyActions {
		return fmt.Errorf("%w: actions must contain between 1 and %d values", ErrInvalidPolicy, MaxPolicyActions)
	}
	if p.Spec.Effect != EffectAllow && p.Spec.Effect != EffectDeny {
		return fmt.Errorf("%w: invalid effect %q", ErrInvalidPolicy, p.Spec.Effect)
	}
	seen := make(map[string]struct{}, len(p.Spec.Actions))
	for _, action := range p.Spec.Actions {
		if _, exists := seen[action]; exists {
			return fmt.Errorf("%w: duplicate action %q", ErrInvalidPolicy, action)
		}
		seen[action] = struct{}{}
		resource, _, err := registry.Resolve(p.Spec.Resource.Type, action)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
		}
		if p.Scope.Type == PolicyScopeTenant && resource.Scope != ResourceScopeTenant {
			return fmt.Errorf("%w: tenant policy cannot authorize non-tenant resource %q", ErrInvalidPolicy, resource.Key)
		}
	}
	return nil
}

func (p *Policy) normalize() {
	p.Metadata.Code = strings.TrimSpace(p.Metadata.Code)
	p.Metadata.Name = strings.TrimSpace(p.Metadata.Name)
	p.Metadata.Description = strings.TrimSpace(p.Metadata.Description)
	p.Scope.TenantID = strings.TrimSpace(p.Scope.TenantID)
	p.Spec.Resource.Type = strings.TrimSpace(p.Spec.Resource.Type)
	for index := range p.Spec.Actions {
		p.Spec.Actions[index] = strings.TrimSpace(p.Spec.Actions[index])
	}
	NormalizeSubjectMatcher(&p.Spec.Subject)
}

// NormalizeSubjectMatcher applies the same canonical authoring rules to
// operation and data-permission policy subjects.
func NormalizeSubjectMatcher(subject *SubjectMatcher) {
	if subject == nil {
		return
	}
	if role := strings.TrimSpace(subject.Role); role != "" {
		subject.Roles.AnyOf = append(subject.Roles.AnyOf, role)
	}
	subject.Role = ""
}

// ValidateSubjectMatcher exposes the shared bounded Subject contract to the
// independent data-permission policy model.
func ValidateSubjectMatcher(subject SubjectMatcher) error { return validateSubject(subject) }

func validateScope(scope PolicyScope) error {
	switch scope.Type {
	case PolicyScopeGlobal:
		if scope.TenantID != "" {
			return fmt.Errorf("%w: global policy must not specify tenant_id", ErrInvalidPolicy)
		}
	case PolicyScopeTenant:
		if !matcherValuePattern.MatchString(scope.TenantID) {
			return fmt.Errorf("%w: tenant policy requires a valid tenant_id", ErrInvalidPolicy)
		}
	default:
		return fmt.Errorf("%w: invalid scope %q", ErrInvalidPolicy, scope.Type)
	}
	return nil
}

func validateSubject(subject SubjectMatcher) error {
	if subject.Role != "" {
		return fmt.Errorf("%w: subject.role must be normalized to roles.any_of", ErrInvalidPolicy)
	}
	sets := []struct {
		name   string
		values []string
	}{
		{name: "types", values: subject.Types},
		{name: "ids", values: subject.IDs},
		{name: "roles.any_of", values: subject.Roles.AnyOf},
		{name: "roles.all_of", values: subject.Roles.AllOf},
		{name: "roles.none_of", values: subject.Roles.NoneOf},
	}
	for _, set := range sets {
		if set.values != nil && len(set.values) == 0 {
			return fmt.Errorf("%w: %s must not be empty", ErrInvalidPolicy, set.name)
		}
		if len(set.values) > MaxMatcherValues {
			return fmt.Errorf("%w: %s exceeds %d values", ErrInvalidPolicy, set.name, MaxMatcherValues)
		}
		seen := make(map[string]struct{}, len(set.values))
		for _, value := range set.values {
			if !matcherValuePattern.MatchString(value) {
				return fmt.Errorf("%w: invalid %s value %q", ErrInvalidPolicy, set.name, value)
			}
			if _, exists := seen[value]; exists {
				return fmt.Errorf("%w: duplicate %s value %q", ErrInvalidPolicy, set.name, value)
			}
			seen[value] = struct{}{}
		}
	}
	for _, denied := range subject.Roles.NoneOf {
		if slices.Contains(subject.Roles.AnyOf, denied) || slices.Contains(subject.Roles.AllOf, denied) {
			return fmt.Errorf("%w: role %q cannot be both required and forbidden", ErrInvalidPolicy, denied)
		}
	}
	return nil
}
