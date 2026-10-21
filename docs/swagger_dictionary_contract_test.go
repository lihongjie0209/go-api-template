package docs

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDictionarySwaggerRequiresChildrenArray(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("swagger.json")
	require.NoError(t, err)
	var document struct {
		Definitions map[string]struct {
			Properties map[string]struct {
				Type  string `json:"type"`
				Items *struct {
					Ref string `json:"$ref"`
				} `json:"items"`
			} `json:"properties"`
			Required []string `json:"required"`
		} `json:"definitions"`
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Schema struct {
					AllOf []struct {
						Properties map[string]struct {
							Ref string `json:"$ref"`
						} `json:"properties"`
					} `json:"allOf"`
				} `json:"schema"`
			} `json:"responses"`
		} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(data, &document))

	item := document.Definitions["dictionary.ResultItem"]
	require.True(t, slices.Contains(item.Required, "children"))
	require.Equal(t, "array", item.Properties["children"].Type)
	require.NotNil(t, item.Properties["children"].Items)
	require.Equal(t, "#/definitions/dictionary.ResultItem", item.Properties["children"].Items.Ref)

	response := document.Paths["/api/v1/public/dictionaries/query"]["post"].Responses["200"].Schema
	require.Condition(t, func() bool {
		for _, component := range response.AllOf {
			if component.Properties["body"].Ref == "#/definitions/dictionary.Result" {
				return true
			}
		}
		return false
	})
}
