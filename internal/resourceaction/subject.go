package resourceaction

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Subject is a trusted identity projection used by data-permission evaluation.
type Subject struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	Authenticated bool     `json:"authenticated"`
	TenantID      string   `json:"tenant_id"`
	MembershipID  string   `json:"membership_id"`
	Roles         []string `json:"roles"`
}

type Resource struct {
	Type     string `json:"type"`
	TenantID string `json:"tenant_id"`
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

var (
	ErrInvalidSubject   = errors.New("resourceaction: invalid subject matcher")
	matcherValuePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,127}$`)
)

const MaxMatcherValues = 128

func NormalizeSubjectMatcher(subject *SubjectMatcher) {
	if subject == nil {
		return
	}
	if role := strings.TrimSpace(subject.Role); role != "" {
		subject.Roles.AnyOf = append(subject.Roles.AnyOf, role)
	}
	subject.Role = ""
}

func ValidateSubjectMatcher(subject SubjectMatcher) error {
	if subject.Role != "" {
		return fmt.Errorf("%w: role must be normalized", ErrInvalidSubject)
	}
	sets := [][]string{subject.Types, subject.IDs, subject.Roles.AnyOf, subject.Roles.AllOf, subject.Roles.NoneOf}
	for _, values := range sets {
		if values != nil && len(values) == 0 || len(values) > MaxMatcherValues {
			return ErrInvalidSubject
		}
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			if !matcherValuePattern.MatchString(value) {
				return ErrInvalidSubject
			}
			if _, exists := seen[value]; exists {
				return ErrInvalidSubject
			}
			seen[value] = struct{}{}
		}
	}
	for _, denied := range subject.Roles.NoneOf {
		if slices.Contains(subject.Roles.AnyOf, denied) || slices.Contains(subject.Roles.AllOf, denied) {
			return ErrInvalidSubject
		}
	}
	return nil
}

func MatchSubject(matcher SubjectMatcher, subject Subject) bool {
	if matcher.Authenticated != nil && *matcher.Authenticated != subject.Authenticated {
		return false
	}
	if len(matcher.Types) > 0 && !slices.Contains(matcher.Types, subject.Type) {
		return false
	}
	if len(matcher.IDs) > 0 && !slices.Contains(matcher.IDs, subject.ID) {
		return false
	}
	roles := make(map[string]struct{}, len(subject.Roles))
	for _, role := range subject.Roles {
		roles[role] = struct{}{}
	}
	if len(matcher.Roles.AnyOf) > 0 && !hasAny(roles, matcher.Roles.AnyOf) {
		return false
	}
	if len(matcher.Roles.AllOf) > 0 && !hasAll(roles, matcher.Roles.AllOf) {
		return false
	}
	return !hasAny(roles, matcher.Roles.NoneOf)
}

func hasAny(actual map[string]struct{}, values []string) bool {
	for _, value := range values {
		if _, ok := actual[value]; ok {
			return true
		}
	}
	return false
}

func hasAll(actual map[string]struct{}, values []string) bool {
	for _, value := range values {
		if _, ok := actual[value]; !ok {
			return false
		}
	}
	return true
}
