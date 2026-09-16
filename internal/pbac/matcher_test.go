package pbac

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchSubject(t *testing.T) {
	t.Parallel()
	authenticated := true
	matcher := SubjectMatcher{
		Authenticated: &authenticated,
		Types:         []string{"user"},
		Roles: RolesMatcher{
			AnyOf:  []string{"department_manager", "tenant_admin"},
			AllOf:  []string{"employee"},
			NoneOf: []string{"suspended_member"},
		},
	}
	tests := []struct {
		name     string
		subject  Subject
		expected bool
	}{
		{name: "matches", subject: Subject{Authenticated: true, Type: "user", Roles: []string{"employee", "department_manager"}}, expected: true},
		{name: "anonymous", subject: Subject{Type: "user", Roles: []string{"employee", "department_manager"}}},
		{name: "wrong type", subject: Subject{Authenticated: true, Type: "service_account", Roles: []string{"employee", "department_manager"}}},
		{name: "missing any role", subject: Subject{Authenticated: true, Type: "user", Roles: []string{"employee"}}},
		{name: "missing required role", subject: Subject{Authenticated: true, Type: "user", Roles: []string{"department_manager"}}},
		{name: "forbidden role", subject: Subject{Authenticated: true, Type: "user", Roles: []string{"employee", "department_manager", "suspended_member"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.expected, MatchSubject(matcher, test.subject))
		})
	}
}
