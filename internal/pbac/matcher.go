package pbac

// Subject contains the trusted identity projection used by operation PBAC.
type Subject struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	Authenticated bool     `json:"authenticated"`
	TenantID      string   `json:"tenant_id"`
	MembershipID  string   `json:"membership_id"`
	Roles         []string `json:"roles"`
}

// Resource identifies the operation-level resource type and tenant boundary.
type Resource struct {
	Type     string `json:"type"`
	TenantID string `json:"tenant_id"`
}

// EvaluationRequest is the complete trusted input to one authorization
// decision.
type EvaluationRequest struct {
	Subject  Subject          `json:"subject"`
	Resource Resource         `json:"resource"`
	Action   string           `json:"action"`
	Context  OperationContext `json:"context"`
}

// OperationContext contains only server-derived attributes approved for
// ActionPolicy `when` expressions. Request bodies and resource data never enter
// this structure.
type OperationContext struct {
	Transport            string `json:"transport"`
	Operation            string `json:"operation"`
	Profile              string `json:"profile"`
	Timezone             string `json:"timezone"`
	LocalHour            int    `json:"local_hour"`
	Weekday              int    `json:"weekday"`
	BusinessDay          bool   `json:"business_day"`
	AuthenticationScheme string `json:"authentication_scheme"`
}

// MatchSubject applies the structural subject matcher.
func MatchSubject(matcher SubjectMatcher, subject Subject) bool {
	if matcher.Authenticated != nil && *matcher.Authenticated != subject.Authenticated {
		return false
	}
	if len(matcher.Types) > 0 && !contains(matcher.Types, subject.Type) {
		return false
	}
	if len(matcher.IDs) > 0 && !contains(matcher.IDs, subject.ID) {
		return false
	}
	roles := make(map[string]struct{}, len(subject.Roles))
	for _, role := range subject.Roles {
		roles[role] = struct{}{}
	}
	if len(matcher.Roles.AnyOf) > 0 && !containsAny(roles, matcher.Roles.AnyOf) {
		return false
	}
	if len(matcher.Roles.AllOf) > 0 && !containsAll(roles, matcher.Roles.AllOf) {
		return false
	}
	if containsAny(roles, matcher.Roles.NoneOf) {
		return false
	}
	return true
}

// MatchResource applies the structural resource type matcher.
func MatchResource(matcher ResourceMatcher, resource Resource) bool {
	return matcher.Type == resource.Type
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsAny(actual map[string]struct{}, expected []string) bool {
	for _, value := range expected {
		if _, ok := actual[value]; ok {
			return true
		}
	}
	return false
}

func containsAll(actual map[string]struct{}, expected []string) bool {
	for _, value := range expected {
		if _, ok := actual[value]; !ok {
			return false
		}
	}
	return true
}
