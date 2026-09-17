package datapermission

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"go.yaml.in/yaml/v3"
)

const MaxPolicyDocumentSize = 64 * 1024

const (
	PolicyAPIVersion = "data-permission.platform/v1"
	PolicyKind       = "DataPermissionPolicy"
)

var (
	ErrInvalidPolicy = errors.New("data permission: invalid policy")
	policyCode       = regexp.MustCompile(`^[a-z][a-z0-9-]{2,127}$`)
)

type PolicyScopeType string

const (
	PolicyScopeGlobal PolicyScopeType = "global"
	PolicyScopeTenant PolicyScopeType = "tenant"
)

type Policy struct {
	APIVersion string         `json:"api_version" yaml:"api_version"`
	Kind       string         `json:"kind" yaml:"kind"`
	Metadata   PolicyMetadata `json:"metadata" yaml:"metadata"`
	Scope      PolicyBoundary `json:"scope" yaml:"scope"`
	Spec       PolicySpec     `json:"spec" yaml:"spec"`
}

type PolicyMetadata struct {
	Code string `json:"code" yaml:"code"`
	Name string `json:"name" yaml:"name"`
}

type PolicyBoundary struct {
	Type     PolicyScopeType `json:"type" yaml:"type"`
	TenantID string          `json:"tenant_id,omitempty" yaml:"tenant_id,omitempty"`
}

type PolicySpec struct {
	Subject           pbac.SubjectMatcher `json:"subject" yaml:"subject"`
	Resource          string              `json:"resource" yaml:"resource"`
	Actions           []string            `json:"actions" yaml:"actions"`
	Condition         string              `json:"condition,omitempty" yaml:"condition,omitempty"`
	ProposedCondition string              `json:"proposed_condition,omitempty" yaml:"proposed_condition,omitempty"`
	Effect            Effect              `json:"effect" yaml:"effect"`
}

type CompiledPolicy struct {
	Policy            Policy
	Predicate         Predicate
	ProposedPredicate Predicate
}

func ParsePolicy(data []byte) (Policy, error) {
	if len(data) == 0 || len(data) > MaxPolicyDocumentSize {
		return Policy{}, ErrInvalidPolicy
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var policy Policy
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Policy{}, fmt.Errorf("%w: multiple documents", ErrInvalidPolicy)
	}
	policy.normalize()
	return policy, nil
}

func MarshalPolicy(policy Policy) ([]byte, error) {
	policy.normalize()
	data, err := yaml.Marshal(policy)
	if err != nil || len(data) > MaxPolicyDocumentSize {
		return nil, ErrInvalidPolicy
	}
	return data, nil
}

func (p *Policy) normalize() {
	p.Metadata.Code = strings.TrimSpace(p.Metadata.Code)
	p.Metadata.Name = strings.TrimSpace(p.Metadata.Name)
	p.Scope.TenantID = strings.TrimSpace(p.Scope.TenantID)
	p.Spec.Resource = strings.TrimSpace(p.Spec.Resource)
	p.Spec.Condition = strings.TrimSpace(p.Spec.Condition)
	p.Spec.ProposedCondition = strings.TrimSpace(p.Spec.ProposedCondition)
	for i := range p.Spec.Actions {
		p.Spec.Actions[i] = strings.TrimSpace(p.Spec.Actions[i])
	}
	pbac.NormalizeSubjectMatcher(&p.Spec.Subject)
}

func clonePolicy(policy Policy) Policy {
	if policy.Spec.Subject.Authenticated != nil {
		authenticated := *policy.Spec.Subject.Authenticated
		policy.Spec.Subject.Authenticated = &authenticated
	}
	policy.Spec.Actions = slices.Clone(policy.Spec.Actions)
	policy.Spec.Subject.Types = slices.Clone(policy.Spec.Subject.Types)
	policy.Spec.Subject.IDs = slices.Clone(policy.Spec.Subject.IDs)
	policy.Spec.Subject.Roles.AnyOf = slices.Clone(policy.Spec.Subject.Roles.AnyOf)
	policy.Spec.Subject.Roles.AllOf = slices.Clone(policy.Spec.Subject.Roles.AllOf)
	policy.Spec.Subject.Roles.NoneOf = slices.Clone(policy.Spec.Subject.Roles.NoneOf)
	return policy
}

func (p Policy) Compile(schemas *SchemaRegistry, resources *pbac.Registry) (CompiledPolicy, error) {
	if p.APIVersion != PolicyAPIVersion || p.Kind != PolicyKind || !policyCode.MatchString(p.Metadata.Code) || strings.TrimSpace(p.Metadata.Name) == "" {
		return CompiledPolicy{}, ErrInvalidPolicy
	}
	if p.Scope.Type == PolicyScopeGlobal && p.Scope.TenantID != "" || p.Scope.Type == PolicyScopeTenant && strings.TrimSpace(p.Scope.TenantID) == "" {
		return CompiledPolicy{}, ErrInvalidPolicy
	}
	if p.Scope.Type != PolicyScopeGlobal && p.Scope.Type != PolicyScopeTenant || (p.Spec.Effect != EffectAllow && p.Spec.Effect != EffectDeny) || len(p.Spec.Actions) == 0 || len(p.Spec.Actions) > 64 {
		return CompiledPolicy{}, ErrInvalidPolicy
	}
	if err := pbac.ValidateSubjectMatcher(p.Spec.Subject); err != nil {
		return CompiledPolicy{}, fmt.Errorf("%w: subject: %v", ErrInvalidPolicy, err)
	}
	schema, ok := schemas.Get(p.Spec.Resource)
	if !ok || resources == nil {
		return CompiledPolicy{}, ErrInvalidSchema
	}
	actions := slices.Clone(p.Spec.Actions)
	slices.Sort(actions)
	if len(slices.Compact(actions)) != len(p.Spec.Actions) {
		return CompiledPolicy{}, ErrInvalidPolicy
	}
	for _, action := range p.Spec.Actions {
		resource, _, err := resources.Resolve(p.Spec.Resource, action)
		if err != nil || p.Scope.Type == PolicyScopeTenant && resource.Scope != pbac.ResourceScopeTenant {
			return CompiledPolicy{}, fmt.Errorf("%w: resource action", ErrInvalidPolicy)
		}
		if p.Spec.ProposedCondition != "" && action != "create" && action != "add" && action != "update" {
			return CompiledPolicy{}, fmt.Errorf("%w: proposed_condition is unsupported for action %q", ErrInvalidPolicy, action)
		}
	}
	parser, err := NewConditionParser(schema)
	if err != nil {
		return CompiledPolicy{}, err
	}
	predicate, err := parser.Parse(p.Spec.Condition)
	if err != nil {
		return CompiledPolicy{}, err
	}
	proposedPredicate, err := parser.ParseProposed(p.Spec.ProposedCondition)
	if err != nil {
		return CompiledPolicy{}, err
	}
	return CompiledPolicy{Policy: p, Predicate: predicate, ProposedPredicate: proposedPredicate}, nil
}
