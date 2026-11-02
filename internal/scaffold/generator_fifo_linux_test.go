//go:build linux

package scaffold

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCopyLocalRejectsNamedPipeWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	pipe := filepath.Join(source, "template.pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- copyLocal(context.Background(), source, filepath.Join(root, "destination")) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("named pipe error = %v, want unsupported file type", err)
		}
	case <-time.After(200 * time.Millisecond):
		writer, err := os.OpenFile(pipe, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_ = writer.Close()
		<-done
		t.Fatal("copyLocal blocked opening a named pipe")
	}
}
