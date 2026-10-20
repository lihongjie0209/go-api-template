package docs

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestApplicationPageFiltersAreOptional(t *testing.T) {
	data, err := os.ReadFile("swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Definitions map[string]struct {
			Required []string `json:"required"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		fields []string
	}{
		{name: "httptransport.ApplicationPageRequest", fields: []string{"ids", "codes"}},
		{name: "httptransport.TenantApplicationPageRequest", fields: []string{"application_ids"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			definition, ok := spec.Definitions[tt.name]
			if !ok {
				t.Fatalf("missing Swagger definition %q", tt.name)
			}
			for _, field := range tt.fields {
				if slices.Contains(definition.Required, field) {
					t.Errorf("optional filter %q is marked required", field)
				}
			}
		})
	}
}
