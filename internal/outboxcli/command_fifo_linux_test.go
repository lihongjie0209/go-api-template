//go:build linux

package outboxcli

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/eventbus"
)

func TestLoadReplacementEnvelopeRejectsNamedPipeWithoutWaitingForWriter(t *testing.T) {
	root := t.TempDir()
	pipe := filepath.Join(root, "envelope.pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked-envelope.pipe")
	if err := os.Symlink(pipe, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{pipe, link} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				_, err := loadReplacementEnvelope(path)
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, eventbus.ErrRepairInvalid) {
					t.Fatalf("named pipe error = %v, want invalid repair request", err)
				}
			case <-time.After(200 * time.Millisecond):
				writer, err := os.OpenFile(pipe, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_ = writer.Close()
				<-done
				t.Fatal("named pipe input blocked waiting for a writer")
			}
		})
	}
}
