package docs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEveryPageRouteDocumentsTypedResult(t *testing.T) {
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
	count := 0
	for path, methods := range document.Paths {
		if !strings.HasSuffix(path, "/page") {
			continue
		}
		count++
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			post, ok := methods["post"]
			require.True(t, ok, "page route must use POST")
			response, ok := post.Responses["200"]
			require.True(t, ok)
			bodyRef := ""
			for _, component := range response.Schema.AllOf {
				if ref := component.Properties["body"].Ref; ref != "" {
					bodyRef = ref
					break
				}
			}
			require.True(t, strings.HasPrefix(bodyRef, "#/definitions/"), "missing typed response body")
			modelName := strings.TrimPrefix(bodyRef, "#/definitions/")
			model, exists := document.Definitions[modelName]
			require.True(t, exists, "missing model %s", modelName)
			require.Equal(t, "array", model.Properties["items"].Type)
			require.NotNil(t, model.Properties["items"].Items)
			require.True(t, strings.HasPrefix(model.Properties["items"].Items.Ref, "#/definitions/"), "items must reference a typed model")
			for _, field := range []string{"page", "page_size", "total"} {
				require.Equal(t, "integer", model.Properties[field].Type, field)
			}
		})
	}
	require.NotZero(t, count, "no page routes found in generated Swagger")
}
