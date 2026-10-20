package httptransport

import (
	"reflect"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/serviceaccount"
	"github.com/lihongjie0209/go-api-template/internal/tenant"
)

func TestDocumentedPageResponsesMatchRuntimeResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		runtime    reflect.Type
		documented reflect.Type
	}{
		{name: "users", runtime: reflect.TypeFor[pagination.Result[identity.User]](), documented: reflect.TypeFor[UserPageResponse]()},
		{name: "service accounts", runtime: reflect.TypeFor[pagination.Result[serviceaccount.Account]](), documented: reflect.TypeFor[ServiceAccountPageResponse]()},
		{name: "tenant members", runtime: reflect.TypeFor[pagination.Result[tenant.Member]](), documented: reflect.TypeFor[TenantMemberPageResponse]()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.documented.NumField() != test.runtime.NumField() {
				t.Fatalf("documented fields = %d, runtime fields = %d", test.documented.NumField(), test.runtime.NumField())
			}
			for index := range test.runtime.NumField() {
				actual := test.runtime.Field(index)
				doc := test.documented.Field(index)
				if doc.Name != actual.Name || doc.Type != actual.Type || doc.Tag.Get("json") != actual.Tag.Get("json") {
					t.Errorf("field %d: documentation %s %s %q differs from runtime %s %s %q",
						index, doc.Name, doc.Type, doc.Tag.Get("json"), actual.Name, actual.Type, actual.Tag.Get("json"))
				}
			}
		})
	}
}
