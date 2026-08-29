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
