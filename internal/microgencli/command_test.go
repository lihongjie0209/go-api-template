package microgencli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	t.Parallel()
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
