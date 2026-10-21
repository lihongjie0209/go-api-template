package idempotency

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/redis/go-redis/v9"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestFinishSpanDoesNotExposeRedisErrorDetails(t *testing.T) {
	t.Parallel()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	_, span := provider.Tracer("idempotency-test").Start(t.Context(), "idempotency.begin")
	finishSpan(span, "error", errors.New("redis URL with secret-value"))
	spans := recorder.Ended()
	if len(spans) != 1 || len(spans[0].Events()) != 1 {
		t.Fatalf("spans=%d events=%v", len(spans), spans)
	}
	foundSafeCode := false
	for _, attribute := range spans[0].Events()[0].Attributes {
		message := attribute.Value.AsString()
		if strings.Contains(message, "secret-value") {
			t.Fatalf("trace event leaked Redis detail: %v", attribute)
		}
		if attribute.Key == "exception.message" && message == "idempotency_operation_failed" {
			foundSafeCode = true
		}
	}
	if !foundSafeCode {
		t.Fatalf("trace event lacks safe failure code: %v", spans[0].Events()[0].Attributes)
	}
}

func TestManagerSeparatesEnvironmentNamespaces(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	newManager := func(profile string) *Manager {
		return New(client, config.Config{
			Runtime: config.Runtime{ActiveProfile: profile},
			App:     config.App{Name: "orders-service"},
			Idempotency: config.Idempotency{
				Enabled: true, ProcessingTTL: time.Minute,
				ResultTTL: time.Hour, FailureTTL: time.Minute,
				MaxResponseBytes: 1024,
			},
		}, nil)
	}

	development := newManager("development")
	production := newManager("production")
	first, err := development.Begin(context.Background(), "request-0001", "same-request")
	if err != nil || first.State != StateAcquired {
		t.Fatalf("development Begin() = %+v, %v", first, err)
	}
	second, err := production.Begin(context.Background(), "request-0001", "same-request")
	if err != nil || second.State != StateAcquired {
		t.Fatalf("production Begin() = %+v, %v; environments must be isolated", second, err)
	}

	keys := server.Keys()
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("Redis keys = %v, want distinct environment-scoped keys", keys)
	}
}

func TestManagerFingerprintIncludesServiceWhenNamespaceIsShared(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	newManager := func(service string) *Manager {
		return New(client, config.Config{
			Runtime: config.Runtime{ActiveProfile: "production"},
			App:     config.App{Name: service},
			Redis:   config.Redis{KeyPrefix: "shared:"},
			Idempotency: config.Idempotency{
				Enabled: true, ProcessingTTL: time.Minute,
				ResultTTL: time.Hour, FailureTTL: time.Minute,
				MaxResponseBytes: 1024,
			},
		}, nil)
	}

	orders := newManager("orders-service")
	payments := newManager("payments-service")
	first, err := orders.Begin(context.Background(), "request-0001", "same-request")
	if err != nil || first.State != StateAcquired {
		t.Fatalf("orders Begin() = %+v, %v", first, err)
	}
	second, err := payments.Begin(context.Background(), "request-0001", "same-request")
	if err != nil || second.State != StateConflict {
		t.Fatalf("payments Begin() = %+v, %v; shared namespaces must not replay across services", second, err)
	}
}

func TestManagerRejectsStaleOwnerAndRecoversAfterExpiry(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	manager := New(client, config.Config{
		Runtime: config.Runtime{ActiveProfile: "test"}, App: config.App{Name: "orders-service"},
		Idempotency: config.Idempotency{
			Enabled: true, ProcessingTTL: time.Second,
			ResultTTL: time.Hour, FailureTTL: time.Minute, MaxResponseBytes: 16,
		},
	}, nil)

	first, err := manager.Begin(t.Context(), "request-0001", "fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Complete(t.Context(), "request-0001", "stale-owner", map[string]string{"id": "1"}); err == nil {
		t.Fatal("Complete() accepted a stale owner")
	}
	server.FastForward(time.Second)
	retried, err := manager.Begin(t.Context(), "request-0001", "fingerprint")
	if err != nil || retried.State != StateAcquired || retried.Owner == first.Owner {
		t.Fatalf("retry after expiry = %+v, %v", retried, err)
	}
}

func TestManagerLeaseLossCancelsBusinessContext(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	manager := New(client, config.Config{
		Runtime: config.Runtime{ActiveProfile: "test"}, App: config.App{Name: "orders-service"},
		Idempotency: config.Idempotency{
			Enabled: true, ProcessingTTL: 60 * time.Millisecond,
			ResultTTL: time.Hour, FailureTTL: time.Minute, MaxResponseBytes: 1024,
		},
	}, nil)
	decision, err := manager.Begin(t.Context(), "request-lease", "fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	leaseCtx, stop, err := manager.StartLease(t.Context(), "request-lease", decision.Owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range server.Keys() {
		server.Del(key)
	}
	select {
	case <-leaseCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("business context was not canceled after lease loss")
	}
	if err := stop(); err == nil || !errors.Is(context.Cause(leaseCtx), err) {
		t.Fatalf("stop error = %v, context cause = %v", err, context.Cause(leaseCtx))
	}
}
