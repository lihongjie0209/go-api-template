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
	registeredPattern := regexp.MustCompile(`(?:public|jwt)\("([^"]+)"`)
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

func TestPrivilegedPasswordResetIsPBACProtected(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	registration := `jwt("/auth/password/reset", "identity.user", "reset-password", accesscontrol.DataPermissionNone`
	if !strings.Contains(string(source), registration) {
		t.Fatalf("password reset must be registered as the identity.user:reset-password PBAC operation")
	}
	if strings.Contains(string(source), `public("/auth/password/reset"`) {
		t.Fatal("password reset must never bypass PBAC as a public route")
	}
}

func TestForceLogoutAnotherUserUsesPlatformUserPermission(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	registration := `jwt("/auth/sessions/force-logout-all", "identity.user", "force-logout", accesscontrol.DataPermissionNone`
	if !strings.Contains(string(source), registration) {
		t.Fatal("forced logout of another user must use the platform identity.user:force-logout permission")
	}
}

func TestTenantCeilingMutationUsesPlatformScope(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	want := `jwt("/platform/tenant-authorization/permissions/set", "tenant", "grant", accesscontrol.DataPermissionNone`
	if !strings.Contains(string(source), want) {
		t.Fatal("tenant permission ceiling must be a platform-scoped tenant:grant operation")
	}
	if strings.Contains(string(source), `jwt("/tenant-authorization/permissions/set"`) {
		t.Fatal("tenant-scoped route cannot mutate the platform-owned tenant permission ceiling")
	}
}

func TestTenantCreatesRequireObjectDataPermission(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, registration := range []string{
		`jwt("/tenant-members/add", "tenant.member", "add", accesscontrol.DataPermissionObject`,
		`jwt("/tenant-departments/create", "tenant.department", "create", accesscontrol.DataPermissionObject`,
		`jwt("/tenant-roles/create", "tenant.role", "create", accesscontrol.DataPermissionObject`,
	} {
		if !strings.Contains(string(source), registration) {
			t.Errorf("missing object-level data permission registration %q", registration)
		}
	}
}

func TestSwaggerSecurityMatchesEndpointAuthenticationMode(t *testing.T) {
	t.Parallel()
	serverSource, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	documentBytes, err := os.ReadFile("../../../docs/swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]map[string]struct {
			Security []map[string][]string `json:"security"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(documentBytes, &document); err != nil {
		t.Fatal(err)
	}
	for _, definition := range []struct {
		name          string
		pattern       *regexp.Regexp
		wantProtected bool
	}{
		{name: "public", pattern: regexp.MustCompile(`public\("([^"]+)"`), wantProtected: false},
		{name: "jwt", pattern: regexp.MustCompile(`jwt\("([^"]+)"`), wantProtected: true},
	} {
		for _, match := range definition.pattern.FindAllStringSubmatch(string(serverSource), -1) {
			path := "/api/v1" + match[1]
			operation, ok := document.Paths[path]["post"]
			if !ok {
				t.Errorf("%s route %s is missing from Swagger", definition.name, path)
				continue
			}
			protected := len(operation.Security) > 0
			if protected != definition.wantProtected {
				t.Errorf("%s route %s Swagger protected=%v, want %v", definition.name, path, protected, definition.wantProtected)
			}
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
