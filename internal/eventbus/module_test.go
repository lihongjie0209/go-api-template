package eventbus

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/observability"
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

func TestNewBusDoesNotExposeConnectionDetails(t *testing.T) {
	t.Parallel()
	lifecycle := new(captureLifecycle)
	cfg := config.Config{EventBus: config.EventBus{Enabled: true, StartupTimeout: time.Second}}
	bus, err := newBusWithFactory(lifecycle, cfg, func(context.Context, config.Config) (*Bus, error) {
		return nil, errors.New("connect nats://user:secret-value@example.internal:4222 failed")
	})
	if bus != nil || !errors.Is(err, ErrStartupUnavailable) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("unsafe event bus startup result: bus=%v error=%v", bus, err)
	}
	if reason, ok := observability.SafeReason(err); !ok || reason != "event_bus_startup_unavailable" {
		t.Fatalf("startup failure reason = %q, %t; want classified event-bus reason", reason, ok)
	}
	if lifecycle.hook.OnStop != nil {
		t.Fatal("failed event bus constructor registered a stop hook")
	}
}

func TestNewBusRejectsMissingClientAndLateSuccess(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		factory func(context.Context, config.Config) (*Bus, error)
		want    error
	}{
		{name: "missing client", factory: func(context.Context, config.Config) (*Bus, error) { return nil, nil }, want: ErrStartupUnavailable},
		{name: "late success", factory: func(ctx context.Context, _ config.Config) (*Bus, error) {
			<-ctx.Done()
			return nil, nil
		}, want: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := new(captureLifecycle)
			cfg := config.Config{EventBus: config.EventBus{Enabled: true, StartupTimeout: 20 * time.Millisecond}}
			bus, err := newBusWithFactory(lifecycle, cfg, test.factory)
			if bus != nil || !errors.Is(err, test.want) || lifecycle.hook.OnStop != nil {
				t.Fatalf("invalid constructor result accepted: bus=%v error=%v stop hook registered=%t", bus, err, lifecycle.hook.OnStop != nil)
			}
		})
	}
}
