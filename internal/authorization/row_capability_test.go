package authorization

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/stretchr/testify/require"
)

func TestRowCapabilityRegistryRejectsDuplicateResources(t *testing.T) {
	t.Parallel()
	provider := staticRowCapabilityProvider{resource: "tenant.member"}
	resources, schemas := rowCapabilityContracts(t, "tenant.member", pbac.ResourceScopeTenant)
	_, err := NewRowCapabilityRegistry([]RowCapabilityProvider{provider, provider}, resources, schemas)
	require.ErrorIs(t, err, ErrRowCapabilityProviderDuplicate)
}

func TestRowCapabilityRegistryRejectsMissingOrNonTenantContracts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		resource  string
		scope     pbac.ResourceScope
		addSchema bool
	}{
		{name: "unknown resource", resource: "tenant.unknown", scope: pbac.ResourceScopeTenant, addSchema: true},
		{name: "platform resource", resource: "identity.user", scope: pbac.ResourceScopePlatform, addSchema: true},
		{name: "missing schema", resource: "tenant.member", scope: pbac.ResourceScopeTenant},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definitions := pbac.ResourceDefinitions{}
			if test.name != "unknown resource" {
				definitions = append(definitions, pbac.ResourceDefinition{Key: test.resource, Name: test.resource, Scope: test.scope, Actions: []pbac.ActionDefinition{{Key: "read", Name: "read"}}})
			}
			resources, err := pbac.NewRegistryFromDefinitions(definitions)
			require.NoError(t, err)
			schemas, err := datapermission.NewSchemaRegistry()
			require.NoError(t, err)
			if test.addSchema {
				schema, schemaErr := datapermission.NewSchema(test.resource, map[string]datapermission.Field{"id": {Column: "t.id", Type: datapermission.ValueTypeText}})
				require.NoError(t, schemaErr)
				schemas, err = datapermission.NewSchemaRegistry(schema)
				require.NoError(t, err)
			}
			_, err = NewRowCapabilityRegistry([]RowCapabilityProvider{staticRowCapabilityProvider{resource: test.resource}}, resources, schemas)
			require.ErrorIs(t, err, ErrRowCapabilityProviderInvalid)
		})
	}
}

func rowCapabilityContracts(t *testing.T, resource string, scope pbac.ResourceScope) (*pbac.Registry, *datapermission.SchemaRegistry) {
	t.Helper()
	resources, err := pbac.NewRegistryFromDefinitions(pbac.ResourceDefinitions{{Key: resource, Name: resource, Scope: scope, Actions: []pbac.ActionDefinition{{Key: "read", Name: "read"}}}})
	require.NoError(t, err)
	schema, err := datapermission.NewSchema(resource, map[string]datapermission.Field{"id": {Column: "t.id", Type: datapermission.ValueTypeText}})
	require.NoError(t, err)
	schemas, err := datapermission.NewSchemaRegistry(schema)
	require.NoError(t, err)
	return resources, schemas
}

func TestTenantRoleCapabilityProviderLoadsTenantScopedRows(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	provider := NewTenantRoleCapabilityProvider(sqlx.NewDb(raw, "sqlmock"))
	mock.ExpectQuery(`SELECT id,code,name,status,created_by FROM tenant_roles WHERE tenant_id=\? AND id IN \(\?, \?\) AND deleted_at IS NULL`).
		WithArgs("tenant-1", "role-1", "missing-role").
		WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "status", "created_by"}).AddRow("role-1", "manager", "Manager", "active", "user-1"))

	rows, err := provider.Load(t.Context(), "tenant-1", []string{"role-1", "missing-role"})
	require.NoError(t, err)
	require.Equal(t, "manager", rows["role-1"]["code"])
	require.NotContains(t, rows, "missing-role")
	require.NoError(t, mock.ExpectationsWereMet())
}
