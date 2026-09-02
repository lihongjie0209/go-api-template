package httptransport

import (
	"os"
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
