package eventbus

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"go.uber.org/fx"
)

type captureLifecycle struct {
	hook fx.Hook
}

func (l *captureLifecycle) Append(hook fx.Hook) {
	l.hook = hook
}

func TestRegisterConsumerStartsAsynchronouslyAndJoinsOnStop(t *testing.T) {
	lifecycle := new(captureLifecycle)
	started := make(chan struct{})
	returned := make(chan struct{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	RegisterConsumer(lifecycle, "test", logger, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(returned)
		return ctx.Err()
	})

	startCtx, cancelStart := context.WithTimeout(context.Background(), time.Second)
	defer cancelStart()
	if err := lifecycle.hook.OnStart(startCtx); err != nil {
		t.Fatalf("OnStart() error = %v", err)
	}
	select {
	case <-started:
	case <-startCtx.Done():
		t.Fatal("consumer did not start")
	}
	if err := lifecycle.hook.OnStop(context.Background()); err != nil {
		t.Fatalf("OnStop() error = %v", err)
	}
	select {
	case <-returned:
	default:
		t.Fatal("OnStop() returned before the consumer")
	}
}
