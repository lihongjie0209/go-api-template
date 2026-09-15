package microgencli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	command := NewCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"version"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "version=") || !strings.Contains(output.String(), "commit=") {
		t.Fatalf("version output = %q", output.String())
	}
}

func TestReadConfigFromCurrentDirectory(t *testing.T) {
	directory := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	config := "name: orders-service\nmodule: github.com/acme/orders-service\ndatabase-name: orders_db\n"
	if err := os.WriteFile(filepath.Join(directory, ".microgen.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	command := newProjectCommand()
	command.SetArgs([]string{"--source", filepath.Join(directory, "missing"), "--git-init=false", "--tidy=false", "--generate=false"})
	err = command.Execute()
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("Execute() error = %v, want source path error after loading config defaults", err)
	}
}

func TestNewCommand_FlagOverridesEnvironmentAndConfig(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	source := filepath.Join(directory, "template")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module github.com/lihongjie0209/go-api-template\n\ngo 1.25.13\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := "name: config-service\nmodule: github.com/acme/config-service\n"
	if err := os.WriteFile(filepath.Join(directory, ".microgen.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MICROGEN_NAME", "environment-service")
	output := filepath.Join(directory, "generated")
	command := NewCommand()
	command.SetArgs([]string{
		"new", "--name", "flag-service", "--module", "github.com/acme/flag-service",
		"--source", source, "--output", output, "--git-init=false", "--tidy=false", "--generate=false",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "module github.com/acme/flag-service") {
		t.Fatalf("go.mod = %q", data)
	}
}

func TestNewCommand_RefusesToOverwriteOutput(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "existing")
	if err := os.Mkdir(output, 0o755); err != nil {
		t.Fatal(err)
	}
	command := NewCommand()
	command.SetArgs([]string{
		"new", "--name", "orders-service", "--module", "github.com/acme/orders-service",
		"--source", filepath.Join(directory, "unused"), "--output", output,
	})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "output already exists") {
		t.Fatalf("Execute() error = %v", err)
	}
}
