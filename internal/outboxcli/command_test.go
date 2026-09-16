package outboxcli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/eventbus"
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

func TestDeadCommandRequiresActor(t *testing.T) {
	command := newCommand(func(context.Context, listOptions) (eventbus.DeadPage, error) {
		return eventbus.DeadPage{}, nil
	}, func(context.Context, replayOptions) error { return nil })
	command.SetArgs([]string{"dead", "list"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "actor") {
		t.Fatalf("Execute() error = %v", err)
	}
}
