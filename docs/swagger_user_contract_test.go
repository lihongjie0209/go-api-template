package docs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type pageResponseSchema struct {
	AllOf      []pageResponseSchema          `json:"allOf"`
	Items      *pageResponseSchema           `json:"items"`
	Properties map[string]pageResponseSchema `json:"properties"`
	Ref        string                        `json:"$ref"`
	Type       string                        `json:"type"`
}

func TestPageSwaggerResponsesDescribeItems(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Definitions map[string]pageResponseSchema `json:"definitions"`
		Paths       map[string]map[string]struct {
			Responses map[string]struct {
				Schema pageResponseSchema `json:"schema"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	for path, methods := range document.Paths {
		if !strings.HasSuffix(path, "/page") {
			continue
		}
		if methods["post"].Responses["200"].Schema.Ref == "#/definitions/httptransport.Response" {
			t.Errorf("%s has an untyped success body", path)
		}
	}

	tests := []struct {
		name    string
		path    string
		pageRef string
		itemRef string
	}{
		{name: "users", path: "/api/v1/users/page", pageRef: "httptransport.UserPageResponse", itemRef: "identity.User"},
		{name: "service accounts", path: "/api/v1/service-accounts/page", pageRef: "httptransport.ServiceAccountPageResponse", itemRef: "serviceaccount.Account"},
		{name: "tenant members", path: "/api/v1/tenant-members/page", pageRef: "httptransport.TenantMemberPageResponse", itemRef: "tenant.Member"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			response := document.Paths[test.path]["post"].Responses["200"].Schema
			var body pageResponseSchema
			for _, component := range response.AllOf {
				if candidate, ok := component.Properties["body"]; ok {
					body = candidate
				}
			}
			if body.Ref != "#/definitions/"+test.pageRef {
				t.Fatalf("%s response body = %q", test.path, body.Ref)
			}
			page := document.Definitions[test.pageRef]
			if page.Properties["items"].Items == nil ||
				page.Properties["items"].Items.Ref != "#/definitions/"+test.itemRef {
				t.Fatalf("%s items must reference %s", test.path, test.itemRef)
			}
			for _, field := range []string{"page", "page_size", "total"} {
				if page.Properties[field].Type != "integer" {
					t.Errorf("%s %s must be an integer", test.path, field)
				}
			}
		})
	}
}
