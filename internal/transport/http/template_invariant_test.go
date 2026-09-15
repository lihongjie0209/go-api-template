package httptransport

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestTemplateHandlerDocumentsOnlyImplementedEndpoints(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"CreateUser godoc", "GetUser godoc", "ListUsers godoc", "UpdateUser godoc", "DeleteUser godoc", "user.User", "user.Page"} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("template handler contains unimplemented endpoint documentation %q", forbidden)
		}
	}
}

func TestEveryRegisteredBusinessPOSTRouteIsInSwagger(t *testing.T) {
	t.Parallel()
	serverSource, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	registeredPattern := regexp.MustCompile(`api\.POST\("([^"]+)"`)
	registered := map[string]struct{}{}
	for _, match := range registeredPattern.FindAllStringSubmatch(string(serverSource), -1) {
		registered["/api/v1"+match[1]] = struct{}{}
	}

	documentBytes, err := os.ReadFile("../../../docs/swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(documentBytes, &document); err != nil {
		t.Fatal(err)
	}
	documented := map[string]struct{}{}
	for route, operations := range document.Paths {
		if strings.HasPrefix(route, "/api/v1/") {
			if _, ok := operations["post"]; ok {
				documented[route] = struct{}{}
			}
		}
	}
	missing := difference(registered, documented)
	stale := difference(documented, registered)
	if len(missing) > 0 || len(stale) > 0 {
		t.Fatalf("Swagger route mismatch: missing=%v stale=%v", missing, stale)
	}
	for route := range registered {
		var operation struct {
			Responses map[string]struct {
				Schema json.RawMessage `json:"schema"`
			} `json:"responses"`
		}
		if err := json.Unmarshal(document.Paths[route]["post"], &operation); err != nil {
			t.Fatalf("decode operation %s: %v", route, err)
		}
		unified := false
		for status, response := range operation.Responses {
			if strings.HasPrefix(status, "2") && bytes.Contains(response.Schema, []byte(`httptransport.Response`)) {
				unified = true
				break
			}
		}
		if !unified {
			t.Errorf("POST %s has no documented successful common response envelope", route)
		}
	}
}

func difference(left, right map[string]struct{}) []string {
	result := make([]string, 0)
	for value := range left {
		if _, ok := right[value]; !ok {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}
