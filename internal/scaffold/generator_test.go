package scaffold

import (
	"context"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateFromLocalTemplate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "template")
	writeFixture(t, source, "go.mod", "module "+templateModule+"\n\ngo 1.25.0\n")
	writeFixture(t, source, "main.go", "package main\nimport x \""+templateModule+"/internal/example\"\nvar _ = x.Name\nconst service = \""+templateName+"\"\n")
	writeFixture(t, source, "deployments/app.yaml", "namespace: "+templateNamespace+"\nimage: "+templateImage+"\ntable: "+templateMigrationTable+"\n")
	writeFixture(t, source, "config/config-development.yaml", "app:\n  name: "+templateName+"\n")
	writeFixture(t, source, "config/config-test.yaml", "app:\n  name: "+templateName+"\n")
	writeFixture(t, source, "config/config-production.yaml", "app:\n  name: "+templateName+"\n")
	writeFixture(t, source, ".git/config", "must not be copied")
	writeFixture(t, source, "cmd/microgen/main.go", "package main")
	output := filepath.Join(root, "orders")

	generated, err := Generate(context.Background(), Options{Name: "orders-service", Namespace: "commerce", Module: "github.com/acme/orders-service", Output: output, Source: source, InitGit: false})
	if err != nil {
		t.Fatal(err)
	}
	if generated != output {
		t.Fatalf("generated path = %q, want %q", generated, output)
	}
	assertContains(t, filepath.Join(output, "go.mod"), "module github.com/acme/orders-service")
	assertContains(t, filepath.Join(output, "main.go"), `"github.com/acme/orders-service/internal/example"`)
	assertContains(t, filepath.Join(output, "deployments/app.yaml"), "namespace: commerce")
	assertContains(t, filepath.Join(output, "deployments/app.yaml"), "image: ghcr.io/acme/orders-service")
	assertContains(t, filepath.Join(output, "deployments/app.yaml"), "table: orders_service_schema_migrations")
	for _, profile := range []string{"development", "test", "production"} {
		if _, err := os.Stat(filepath.Join(output, "config", "config-"+profile+".yaml")); err != nil {
			t.Fatalf("profile %s: %v", profile, err)
		}
	}
	if _, err := os.Stat(filepath.Join(output, ".git")); !os.IsNotExist(err) {
		t.Fatalf("source .git was copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "cmd", "microgen")); !os.IsNotExist(err) {
		t.Fatalf("microgen was copied: %v", err)
	}
	goSource, err := os.ReadFile(filepath.Join(output, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := format.Source(goSource)
	if err != nil || string(formatted) != string(goSource) {
		t.Fatalf("generated Go source is not formatted: %v", err)
	}
}

func TestOptionsValidation(t *testing.T) {
	t.Parallel()
	options := Options{Name: "Invalid_Name", Module: "not a module", Output: "out"}
	options.Defaults()
	if err := options.Validate(); err == nil {
		t.Fatal("Validate() error = nil")
	}
}

func writeFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertContains(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), expected) {
		t.Fatalf("%s does not contain %q:\n%s", path, expected, data)
	}
}
