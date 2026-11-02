package eventbus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
)

func TestNewBusBoundsStreamProvisioning(t *testing.T) {
	t.Parallel()
	lifecycle := new(captureLifecycle)
	cfg := config.Config{EventBus: config.EventBus{Enabled: true, ConnectTimeout: 10 * time.Millisecond, StartupTimeout: 30 * time.Millisecond}}
	called := false
	bus, err := newBusWithFactory(lifecycle, cfg, func(ctx context.Context, _ config.Config) (*Bus, error) {
		called = true
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > cfg.EventBus.StartupTimeout {
			t.Fatalf("event bus constructor received an unbounded Context: %v, %t", deadline, ok)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if !called || bus != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded constructor result = bus:%v error:%v called:%t", bus, err, called)
	}
	if lifecycle.hook.OnStop != nil {
		t.Fatal("failed event bus constructor registered a stop hook")
	}
}
