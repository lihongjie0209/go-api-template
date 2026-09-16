// Package routepolicy loads and evaluates database-owned route authorization policies.
package routepolicy

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrMissing       = errors.New("route policy missing")
	ErrInvalid       = errors.New("route policy invalid")
	ErrDenied        = errors.New("route policy denied")
	permissionKeyRef = regexp.MustCompile(`permissions\s*\[\s*"([a-z][a-z0-9_.:-]{2,127})"\s*\]`)
	permissionAccess = regexp.MustCompile(`permissions\s*\[`)
)

const (
	MaxExpressionBytes = 4096
	MaxPermissionRefs  = 8
	MaxEvaluationCost  = 1000
)

type Permission struct {
	ID       string
	Key      string
	Resource string
	Action   string
	Scope    platformauthz.Scope
}

type Definition struct {
	ID          string
	RouteID     string
	Expression  string
	Permissions map[string]Permission
	Version     int64
}

type compiled struct {
	definition Definition
	program    cel.Program
}

type Compiler struct{ environment *cel.Env }

func NewCompiler() (*Compiler, error) {
	environment, err := cel.NewEnv(
		cel.Variable("anonymous", cel.BoolType),
		cel.Variable("authenticated", cel.BoolType),
		cel.Variable("principal_type", cel.StringType),
		cel.Variable("permissions", cel.MapType(cel.StringType, cel.BoolType)),
	)
	if err != nil {
		return nil, fmt.Errorf("create route policy environment: %w", err)
	}
	return &Compiler{environment: environment}, nil
}

func (c *Compiler) Compile(definition Definition) (*compiled, error) {
	expression := strings.TrimSpace(definition.Expression)
	if expression == "" || len(expression) > MaxExpressionBytes {
		return nil, fmt.Errorf("%w: expression length must be between 1 and %d", ErrInvalid, MaxExpressionBytes)
	}
	keys := PermissionKeys(expression)
	if strings.Contains(expression, "permissions") {
		accesses := len(permissionAccess.FindAllStringIndex(expression, -1))
		if accesses == 0 || accesses != len(permissionKeyRef.FindAllStringSubmatch(expression, -1)) {
			return nil, fmt.Errorf("%w: permissions must use static string keys", ErrInvalid)
		}
	}
	if len(keys) > MaxPermissionRefs {
		return nil, fmt.Errorf("%w: expression references more than %d permissions", ErrInvalid, MaxPermissionRefs)
	}
	for _, key := range keys {
		permission, ok := definition.Permissions[key]
		if !ok || permission.Resource == "" || permission.Action == "" {
			return nil, fmt.Errorf("%w: permission %q is not an active permission reference", ErrInvalid, key)
		}
	}
	if len(definition.Permissions) != len(keys) {
		return nil, fmt.Errorf("%w: policy contains unused permission references", ErrInvalid)
	}
	ast, issues := c.environment.Compile(expression)
	if issues.Err() != nil {
		return nil, fmt.Errorf("%w: compile expression: %v", ErrInvalid, issues.Err())
	}
	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("%w: expression must return bool", ErrInvalid)
	}
	program, err := c.environment.Program(ast, cel.CostLimit(MaxEvaluationCost))
	if err != nil {
		return nil, fmt.Errorf("%w: create expression program: %v", ErrInvalid, err)
	}
	return &compiled{definition: definition, program: program}, nil
}

func PermissionKeys(expression string) []string {
	matches := permissionKeyRef.FindAllStringSubmatch(expression, -1)
	keys := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if _, ok := seen[match[1]]; ok {
			continue
		}
		seen[match[1]] = struct{}{}
		keys = append(keys, match[1])
	}
	return keys
}

func (p *compiled) Evaluate(ctx context.Context, authorizer platformauthz.Authorizer) error {
	principal, isAuthenticated := platformprincipal.FromContext(ctx)
	if isAuthenticated && len(p.definition.Permissions) > 0 && authorizer == nil {
		return platformauthz.ErrDecisionUnavailable
	}
	decisions := make(map[string]bool, len(p.definition.Permissions))
	for key, permission := range p.definition.Permissions {
		if !isAuthenticated {
			decisions[key] = false
			continue
		}
		err := authorizer.Authorize(ctx, principal, platformauthz.Requirement{
			Resource: permission.Resource,
			Action:   permission.Action,
			Scope:    permission.Scope,
		})
		if err != nil && !errors.Is(err, platformauthz.ErrDenied) {
			return fmt.Errorf("authorize permission %q: %w", key, err)
		}
		decisions[key] = err == nil
	}
	principalType := ""
	if isAuthenticated {
		principalType = string(principal.Type)
	}
	value, _, err := p.program.ContextEval(ctx, map[string]any{
		"anonymous":      !isAuthenticated,
		"authenticated":  isAuthenticated,
		"principal_type": principalType,
		"permissions":    decisions,
	})
	if err != nil {
		return fmt.Errorf("evaluate route policy: %w", err)
	}
	if value != types.True {
		return ErrDenied
	}
	return nil
}
