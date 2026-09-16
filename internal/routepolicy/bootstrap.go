package routepolicy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"go.yaml.in/yaml/v3"
)

const maxBootstrapPolicies = 1000

type BootstrapManifest struct {
	Version               int                             `yaml:"version"`
	PermissionDefinitions []BootstrapPermissionDefinition `yaml:"permission_definitions"`
	Policies              []BootstrapPolicy               `yaml:"policies"`
}

type BootstrapPermissionDefinition struct {
	Key         string `yaml:"key"`
	ParentKey   string `yaml:"parent_key"`
	Name        string `yaml:"name"`
	NodeType    string `yaml:"node_type"`
	Resource    string `yaml:"resource"`
	Action      string `yaml:"action"`
	Description string `yaml:"description"`
	SortOrder   int64  `yaml:"sort_order"`
	Status      string `yaml:"status"`
}

type BootstrapPolicy struct {
	Protocol    string                `yaml:"protocol"`
	Method      string                `yaml:"method"`
	Path        string                `yaml:"path"`
	Expression  string                `yaml:"expression"`
	Description string                `yaml:"description"`
	Status      string                `yaml:"status"`
	Permissions []BootstrapPermission `yaml:"permissions"`
}

type BootstrapPermission struct {
	Key   string `yaml:"key"`
	Scope string `yaml:"scope"`
}

type BootstrapResult struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
}

func LoadBootstrapManifest(path string) (BootstrapManifest, error) {
	if strings.TrimSpace(path) == "" {
		return BootstrapManifest{}, errors.New("bootstrap manifest path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return BootstrapManifest{}, fmt.Errorf("read bootstrap manifest: %w", err)
	}
	if len(data) == 0 || len(data) > 1<<20 {
		return BootstrapManifest{}, errors.New("bootstrap manifest must be between 1 byte and 1 MiB")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var manifest BootstrapManifest
	if err := decoder.Decode(&manifest); err != nil {
		return BootstrapManifest{}, fmt.Errorf("decode bootstrap manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return BootstrapManifest{}, errors.New("bootstrap manifest must contain exactly one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return BootstrapManifest{}, fmt.Errorf("decode bootstrap manifest trailer: %w", err)
	}
	if manifest.Version != 1 || len(manifest.Policies) == 0 || len(manifest.Policies) > maxBootstrapPolicies {
		return BootstrapManifest{}, errors.New("bootstrap manifest requires version 1 and between 1 and 1000 policies")
	}
	return manifest, nil
}

func Bootstrap(ctx context.Context, db *sqlx.DB, service *Service, actor string, manifest BootstrapManifest) (BootstrapResult, error) {
	actor = strings.TrimSpace(actor)
	if db == nil || service == nil || actor == "" || len(actor) > maxRoutePolicyIDLength || manifest.Version != 1 || len(manifest.Policies) == 0 || len(manifest.Policies) > maxBootstrapPolicies {
		return BootstrapResult{}, errors.New("database, policy service, bounded actor and version 1 manifest are required")
	}
	ctx = platformprincipal.SystemContext(ctx, actor)
	result := BootstrapResult{}
	seenRoutes := make(map[string]struct{}, len(manifest.Policies))
	for index, policy := range manifest.Policies {
		route, err := NewRoute(policy.Protocol, policy.Method, policy.Path, "bootstrap", "manifest-v1")
		if err != nil {
			return result, fmt.Errorf("policy %d route: %w", index, err)
		}
		if _, duplicate := seenRoutes[route.ID]; duplicate {
			return result, fmt.Errorf("policy %d: duplicate route", index)
		}
		seenRoutes[route.ID] = struct{}{}
		references, err := resolveBootstrapPermissions(ctx, db, policy.Permissions)
		if err != nil {
			return result, fmt.Errorf("policy %d permissions: %w", index, err)
		}
		input := SetInput{RouteID: route.ID, Expression: policy.Expression, Description: policy.Description, Status: policy.Status, References: references}
		existing, err := service.Get(ctx, route.ID)
		switch {
		case errors.Is(err, ErrNotFound):
			if _, err := service.Set(ctx, input); err != nil {
				return result, fmt.Errorf("create policy %d: %w", index, err)
			}
			result.Created++
		case err != nil:
			return result, fmt.Errorf("get policy %d: %w", index, err)
		case bootstrapPolicyEqual(existing, input):
			result.Unchanged++
		default:
			input.Version = existing.Version
			if _, err := service.Set(ctx, input); err != nil {
				return result, fmt.Errorf("update policy %d: %w", index, err)
			}
			result.Updated++
		}
	}
	return result, nil
}

func resolveBootstrapPermissions(ctx context.Context, db *sqlx.DB, permissions []BootstrapPermission) ([]ReferenceInput, error) {
	if len(permissions) > MaxPermissionRefs {
		return nil, fmt.Errorf("%w: too many permission references", ErrInvalid)
	}
	result := make([]ReferenceInput, 0, len(permissions))
	seen := make(map[string]struct{}, len(permissions))
	for _, permission := range permissions {
		key := strings.TrimSpace(permission.Key)
		keys := PermissionKeys(`permissions["` + key + `"]`)
		if len(keys) != 1 || keys[0] != key {
			return nil, fmt.Errorf("%w: invalid permission key", ErrInvalid)
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate permission key", ErrInvalid)
		}
		seen[key] = struct{}{}
		if _, err := parseScope(permission.Scope); err != nil {
			return nil, err
		}
		var id string
		query := db.Rebind(`SELECT id FROM permissions WHERE LOWER(permission_key)=? AND node_type='permission' AND status='active' AND deleted_at IS NULL`)
		if err := db.GetContext(ctx, &id, query, strings.ToLower(key)); err != nil {
			return nil, fmt.Errorf("resolve permission %q: %w", key, err)
		}
		result = append(result, ReferenceInput{PermissionID: id, Scope: permission.Scope})
	}
	return result, nil
}

func bootstrapPolicyEqual(existing View, desired SetInput) bool {
	if strings.TrimSpace(existing.Expression) != strings.TrimSpace(desired.Expression) || strings.TrimSpace(existing.Description) != strings.TrimSpace(desired.Description) || existing.Status != desired.Status || len(existing.References) != len(desired.References) {
		return false
	}
	want := make(map[string]string, len(desired.References))
	for _, reference := range desired.References {
		want[reference.PermissionID] = reference.Scope
	}
	for _, reference := range existing.References {
		if want[reference.PermissionID] != reference.Scope {
			return false
		}
	}
	return true
}
