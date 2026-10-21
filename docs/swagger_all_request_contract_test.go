package docs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEveryPOSTBodyHasDocumentedFieldsOrIsExplicitlyEmpty(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("swagger.json")
	require.NoError(t, err)
	var document struct {
		Definitions map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"definitions"`
		Paths map[string]map[string]struct {
			Parameters []struct {
				In       string `json:"in"`
				Required bool   `json:"required"`
				Schema   struct {
					Ref        string                     `json:"$ref"`
					Type       string                     `json:"type"`
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"schema"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(data, &document))
	intentionallyEmpty := map[string]bool{
		"/api/v1/me/navigation-usage":                         true,
		"/api/v1/tenants/delete-target":                       true,
		"/api/v1/tenants/update-target":                       true,
		"/api/v1/scheduled-jobs/handlers/list":                true,
		"/api/v1/profile/get":                                 true,
		"/api/v1/me/applications":                             true,
		"/api/v1/me/application/current":                      true,
		"/api/v1/tenant-authorization/assignable-permissions": true,
	}
	seenEmpty := make(map[string]bool, len(intentionallyEmpty))
	bodyCount := 0
	for path, methods := range document.Paths {
		post, ok := methods["post"]
		if !ok {
			continue
		}
		for _, parameter := range post.Parameters {
			if parameter.In != "body" {
				continue
			}
			bodyCount++
			t.Run(path, func(t *testing.T) {
				t.Parallel()
				require.True(t, parameter.Required, "POST request body must be required")
				if parameter.Schema.Ref == "" {
					require.True(t, intentionallyEmpty[path], "inline request schema requires explicit review")
					require.Equal(t, "object", parameter.Schema.Type)
					require.Empty(t, parameter.Schema.Properties)
					return
				}
				require.True(t, strings.HasPrefix(parameter.Schema.Ref, "#/definitions/"), "POST body must reference a request model")
				modelName := strings.TrimPrefix(parameter.Schema.Ref, "#/definitions/")
				model, exists := document.Definitions[modelName]
				require.True(t, exists, "missing request model %s", modelName)
				if intentionallyEmpty[path] {
					require.Empty(t, model.Properties, "explicitly empty request gained fields; review its contract")
					return
				}
				require.NotEmpty(t, model.Properties, "request fields are missing from Swagger")
			})
			if intentionallyEmpty[path] {
				seenEmpty[path] = true
			}
		}
	}
	require.NotZero(t, bodyCount, "no POST request bodies found in generated Swagger")
	require.Equal(t, intentionallyEmpty, seenEmpty, "explicitly empty request paths changed")
}

func TestEveryPOSTDocumentsEffectiveMediaTypes(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("swagger.json")
	require.NoError(t, err)
	var document struct {
		Consumes []string `json:"consumes"`
		Produces []string `json:"produces"`
		Paths    map[string]map[string]struct {
			Consumes []string `json:"consumes"`
			Produces []string `json:"produces"`
		} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(data, &document))
	require.Contains(t, document.Consumes, "application/json")
	require.Contains(t, document.Produces, "application/json")
	count := 0
	for path, methods := range document.Paths {
		post, ok := methods["post"]
		if !ok {
			continue
		}
		count++
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			consumes := post.Consumes
			if len(consumes) == 0 {
				consumes = document.Consumes
			}
			wantConsumes := "application/json"
			if path == "/api/v1/files/upload" {
				wantConsumes = "multipart/form-data"
			}
			require.Contains(t, consumes, wantConsumes)
			produces := post.Produces
			if len(produces) == 0 {
				produces = document.Produces
			}
			require.Contains(t, produces, "application/json")
		})
	}
	require.NotZero(t, count, "no POST routes found in generated Swagger")
}
