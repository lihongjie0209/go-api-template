package docs

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/application"
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/resourcecatalog"
	"github.com/lihongjie0209/go-api-template/internal/scheduler"
	"github.com/stretchr/testify/require"
)

func TestPaginatedRuntimeJSONContract(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		value any
	}{
		{name: "applications", value: application.Page{Items: []application.Record{}, Page: 1, PageSize: 20}},
		{name: "tenant grants", value: application.GrantPage{Items: []application.Grant{}, Page: 1, PageSize: 20}},
		{name: "platform grants", value: authorization.PlatformGrantPage{Items: []authorization.PlatformGrant{}, Page: 1, PageSize: 20}},
		{name: "resources", value: resourcecatalog.ResourcePage{Items: []resourcecatalog.Resource{}, Page: 1, PageSize: 20}},
		{name: "actions", value: resourcecatalog.ActionPage{Items: []resourcecatalog.Action{}, Page: 1, PageSize: 20}},
		{name: "scheduled jobs", value: scheduler.DefinitionPage{Items: []scheduler.Definition{}, Page: 1, PageSize: 20}},
		{name: "job runs", value: scheduler.RunPage{Items: []scheduler.Run{}, Page: 1, PageSize: 20}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := json.Marshal(test.value)
			require.NoError(t, err)
			require.JSONEq(t, `{"items":[],"page":1,"page_size":20,"total":0}`, string(encoded))
		})
	}
}

func TestPaginatedSwaggerModels(t *testing.T) {
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
	for _, test := range []struct {
		name, route, model, item string
	}{
		{name: "applications", route: "/api/v1/applications/page", model: "application.Page", item: "application.Record"},
		{name: "tenant grants", route: "/api/v1/platform/tenant-applications/page", model: "application.GrantPage", item: "application.Grant"},
		{name: "platform grants", route: "/api/v1/platform/permission-grants/page", model: "authorization.PlatformGrantPage", item: "authorization.PlatformGrant"},
		{name: "resources", route: "/api/v1/authorization/resources/page", model: "resourcecatalog.ResourcePage", item: "resourcecatalog.Resource"},
		{name: "actions", route: "/api/v1/authorization/actions/page", model: "resourcecatalog.ActionPage", item: "resourcecatalog.Action"},
		{name: "scheduled jobs", route: "/api/v1/scheduled-jobs/page", model: "scheduler.DefinitionPage", item: "scheduler.Definition"},
		{name: "job runs", route: "/api/v1/scheduled-job-runs/page", model: "scheduler.RunPage", item: "scheduler.Run"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			model, ok := document.Definitions[test.model]
			require.True(t, ok)
			require.Equal(t, "array", model.Properties["items"].Type)
			require.NotNil(t, model.Properties["items"].Items)
			require.Equal(t, "#/definitions/"+test.item, model.Properties["items"].Items.Ref)
			for _, field := range []string{"page", "page_size", "total"} {
				require.Equal(t, "integer", model.Properties[field].Type, field)
			}
			response := document.Paths[test.route]["post"].Responses["200"].Schema
			require.Condition(t, func() bool {
				for _, component := range response.AllOf {
					if component.Properties["body"].Ref == "#/definitions/"+test.model {
						return true
					}
				}
				return false
			})
		})
	}
}
