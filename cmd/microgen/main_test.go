package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/microgencli"
	"github.com/lihongjie0209/go-api-template/internal/observability"
)

func TestMicrogenCommandErrorRedactsConfigPath(t *testing.T) {
	command := microgencli.NewCommand()
	command.SetArgs([]string{"new", "--config", filepath.Join(t.TempDir(), "secret-value.yaml")})
	err := command.Execute()
	if err == nil {
		t.Fatal("missing configuration unexpectedly succeeded")
	}
	safe := safeCommandError(err)
	if reason, ok := observability.SafeReason(safe); !ok || reason != "microgen_command_failed" || strings.Contains(safe.Error(), "secret-value") {
		t.Fatalf("safe command error = %v, reason = %q", safe, reason)
	}
}
