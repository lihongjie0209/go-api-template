package outboxcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/eventbus"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDeadListCommandEncodesResult(t *testing.T) {
	command := newCommand(func(_ context.Context, options listOptions) (eventbus.DeadPage, error) {
		if options.actor != "operator" || options.page != 2 || options.pageSize != 10 || options.keyword != "publish" {
			t.Fatalf("options = %+v", options)
		}
		return eventbus.DeadPage{Page: 2, PageSize: 10, Total: 1}, nil
	}, func(context.Context, replayOptions) error { return errors.New("unexpected replay") })
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"dead", "list", "--actor", "operator", "--page", "2", "--page-size", "10", "--keyword", "publish"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"total":1`) {
		t.Fatalf("output = %q", output.String())
	}
}

func TestDeadReplayCommandRequiresAndPassesOptimisticVersion(t *testing.T) {
	called := false
	command := newCommand(func(context.Context, listOptions) (eventbus.DeadPage, error) {
		return eventbus.DeadPage{}, errors.New("unexpected list")
	}, func(_ context.Context, options replayOptions) error {
		called = true
		if options.actor != "operator" || options.id != "event-1" || options.version != 4 {
			t.Fatalf("options = %+v", options)
		}
		return nil
	})
	command.SetArgs([]string{"dead", "replay", "--actor", "operator", "--id", "event-1", "--version", "4"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("replay runner was not called")
	}
}

func TestDeadReplayCommandAcceptsCorrectedEnvelopeFile(t *testing.T) {
	called := false
	command := newCommand(func(context.Context, listOptions) (eventbus.DeadPage, error) {
		return eventbus.DeadPage{}, errors.New("unexpected list")
	}, func(_ context.Context, options replayOptions) error {
		called = true
		if options.envelopeFile != "/tmp/corrected.json" || options.id != "event-1" || options.version != 4 {
			t.Fatalf("replay options = %+v", options)
		}
		return nil
	})
	command.SetArgs([]string{"dead", "replay", "--actor", "operator", "--id", "event-1", "--version", "4", "--envelope-file", "/tmp/corrected.json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("replay runner was not called")
	}
}

func TestLoadReplacementEnvelopeBoundsAndValidatesJSON(t *testing.T) {
	valid := &commonv1.EventEnvelope{EventId: "event-1", EventType: "platform.event.v1", AggregateType: "test", SchemaVersion: 1, OccurredAt: timestamppb.Now()}
	data, err := protojson.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		content []byte
		valid   bool
	}{
		{name: "valid", content: data, valid: true},
		{name: "invalid JSON", content: []byte(`{"eventId":`)},
		{name: "oversized file", content: bytes.Repeat([]byte{'x'}, maxReplacementJSONBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "envelope.json")
			if err := os.WriteFile(path, test.content, 0o600); err != nil {
				t.Fatal(err)
			}
			encoded, err := loadReplacementEnvelope(path)
			if !test.valid {
				if !errors.Is(err, eventbus.ErrRepairInvalid) || encoded != nil {
					t.Fatalf("invalid file result = (%d bytes, %v)", len(encoded), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var actual commonv1.EventEnvelope
			if err := proto.Unmarshal(encoded, &actual); err != nil || !proto.Equal(valid, &actual) {
				t.Fatalf("decoded replacement = %v, error = %v", &actual, err)
			}
		})
	}
	if _, err := loadReplacementEnvelope(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, eventbus.ErrRepairInvalid) {
		t.Fatalf("missing file error = %v", err)
	}
	t.Run("symlink to regular file", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "envelope.json")
		link := filepath.Join(root, "mounted-envelope.json")
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := loadReplacementEnvelope(link); err != nil {
			t.Fatalf("regular file symlink rejected: %v", err)
		}
	})
}

func TestDeadCommandRequiresActor(t *testing.T) {
	command := newCommand(func(context.Context, listOptions) (eventbus.DeadPage, error) {
		return eventbus.DeadPage{}, nil
	}, func(context.Context, replayOptions) error { return nil })
	command.SetArgs([]string{"dead", "list"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "actor") {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestDeadListHelpDescribesSafeSearchFields(t *testing.T) {
	command := NewCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"dead", "list", "--help"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "match event ID or subject") || strings.Contains(output.String(), "last error") {
		t.Fatalf("unsafe dead-list keyword help: %q", output.String())
	}
}
