package authorization

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
)

var (
	ErrRowCapabilityProviderInvalid   = errors.New("row capability provider is invalid")
	ErrRowCapabilityProviderDuplicate = errors.New("row capability provider is duplicated")
)

// RowCapabilityProvider is implemented by the module that owns a tenant
// resource. It loads trusted current-row attributes in one bounded query.
type RowCapabilityProvider interface {
	Resource() string
	Load(context.Context, string, []string) (map[string]datapermission.ResourceAttributes, error)
}

type RowCapabilityRegistry struct {
	providers map[string]RowCapabilityProvider
}

func NewRowCapabilityRegistry(providers []RowCapabilityProvider, resources *pbac.Registry, schemas *datapermission.SchemaRegistry) (*RowCapabilityRegistry, error) {
	if resources == nil || schemas == nil {
		return nil, ErrRowCapabilityProviderInvalid
	}
	registry := &RowCapabilityRegistry{providers: make(map[string]RowCapabilityProvider, len(providers))}
	for _, provider := range providers {
		if provider == nil || provider.Resource() == "" {
			return nil, ErrRowCapabilityProviderInvalid
		}
		resource := provider.Resource()
		definition, ok := resources.Resource(resource)
		if !ok || definition.Scope != pbac.ResourceScopeTenant {
			return nil, fmt.Errorf("%w: resource %s must be registered with tenant scope", ErrRowCapabilityProviderInvalid, resource)
		}
		if _, ok := schemas.Get(resource); !ok {
			return nil, fmt.Errorf("%w: resource %s has no data-permission schema", ErrRowCapabilityProviderInvalid, resource)
		}
		if _, exists := registry.providers[resource]; exists {
			return nil, fmt.Errorf("%w: %s", ErrRowCapabilityProviderDuplicate, resource)
		}
		registry.providers[resource] = provider
	}
	return registry, nil
}

func (r *RowCapabilityRegistry) Provider(resource string) (RowCapabilityProvider, bool) {
	if r == nil {
		return nil, false
	}
	provider, ok := r.providers[resource]
	return provider, ok
}

func (r *RowCapabilityRegistry) Resources() []string {
	if r == nil {
		return nil
	}
	resources := make([]string, 0, len(r.providers))
	for resource := range r.providers {
		resources = append(resources, resource)
	}
	slices.Sort(resources)
	return resources
}

type tenantRoleCapabilityProvider struct{ db *sqlx.DB }

func NewTenantRoleCapabilityProvider(db *sqlx.DB) RowCapabilityProvider {
	return &tenantRoleCapabilityProvider{db: db}
}

func (*tenantRoleCapabilityProvider) Resource() string { return "tenant.role" }

func (p *tenantRoleCapabilityProvider) Load(ctx context.Context, tenantID string, ids []string) (map[string]datapermission.ResourceAttributes, error) {
	if p == nil || p.db == nil || tenantID == "" || len(ids) == 0 {
		return nil, ErrRowCapabilityProviderInvalid
	}
	query, args, err := sqlx.In(`SELECT id,code,name,status,created_by FROM tenant_roles WHERE tenant_id=? AND id IN (?) AND deleted_at IS NULL`, tenantID, ids)
	if err != nil {
		return nil, fmt.Errorf("build tenant role capability query: %w", err)
	}
	rows := []struct {
		ID        string `db:"id"`
		Code      string `db:"code"`
		Name      string `db:"name"`
		Status    string `db:"status"`
		CreatedBy string `db:"created_by"`
	}{}
	if err := p.db.SelectContext(ctx, &rows, p.db.Rebind(query), args...); err != nil {
		return nil, fmt.Errorf("load tenant role capabilities: %w", err)
	}
	result := make(map[string]datapermission.ResourceAttributes, len(rows))
	for _, row := range rows {
		result[row.ID] = datapermission.ResourceAttributes{"id": row.ID, "code": row.Code, "name": row.Name, "status": row.Status, "created_by": row.CreatedBy}
	}
	return result, nil
}
