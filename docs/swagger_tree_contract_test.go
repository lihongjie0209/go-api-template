package docs

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedTreeSchemasRequireNullableParentAndChildren(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("swagger.json")
	require.NoError(t, err)
	var document struct {
		Definitions map[string]struct {
			Properties map[string]struct {
				Type     string `json:"type"`
				Nullable bool   `json:"x-nullable"`
				Items    *struct {
					Ref string `json:"$ref"`
				} `json:"items"`
			} `json:"properties"`
			Required []string `json:"required"`
		} `json:"definitions"`
	}
	require.NoError(t, json.Unmarshal(data, &document))
	for _, schemaName := range []string{
		"navigation.Node",
		"permission.TreeNode",
		"tenant.DepartmentNode",
	} {
		t.Run(schemaName, func(t *testing.T) {
			t.Parallel()
			schema, ok := document.Definitions[schemaName]
			require.True(t, ok)
			require.True(t, slices.Contains(schema.Required, "parent_id"))
			require.True(t, schema.Properties["parent_id"].Nullable)
			require.True(t, slices.Contains(schema.Required, "children"))
			require.Equal(t, "array", schema.Properties["children"].Type)
			require.NotNil(t, schema.Properties["children"].Items)
			require.Equal(t, "#/definitions/"+schemaName, schema.Properties["children"].Items.Ref)
		})
	}
}
