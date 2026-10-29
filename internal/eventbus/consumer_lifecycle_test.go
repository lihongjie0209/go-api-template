package eventbus

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/observability"
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
	RegisterConsumer(lifecycle, "test", logger, nil, func(ctx context.Context) error {
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

func TestConsumerFailureLogOmitsDependencyDetails(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logConsumerFailure(t.Context(), logger, "scheduled-job-manual", errors.New("queue endpoint with secret-value"))
	if bytes.Contains(output.Bytes(), []byte("secret-value")) || !bytes.Contains(output.Bytes(), []byte("event_consumer_failed")) {
		t.Fatalf("consumer log exposed dependency error or omitted safe code: %s", output.String())
	}
}

func TestRegisterConsumerRetriesTransientSubscriptionFailure(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		lifecycle := new(captureLifecycle)
		var attempts atomic.Int32
		var output bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&output, nil))
		metrics := observability.NewMetrics(config.Config{Observability: config.Observability{MetricsEnabled: true}}, nil, nil)
		RegisterConsumer(lifecycle, "test", logger, metrics, func(ctx context.Context) error {
			if attempts.Add(1) == 1 {
				return errors.New("queue credential=secret-value")
			}
			<-ctx.Done()
			return ctx.Err()
		})
		if err := lifecycle.hook.OnStart(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := attempts.Load(); got != 2 {
			t.Fatalf("consumer attempts = %d, want retry after transient failure", got)
		}
		recorder := httptest.NewRecorder()
		metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `event_consumer_restarts_total{consumer="test"} 1`) {
			t.Fatalf("consumer restart metric missing: status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		if err := lifecycle.hook.OnStop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(output.Bytes(), []byte("secret-value")) || !bytes.Contains(output.Bytes(), []byte("event_consumer_failed")) {
			t.Fatalf("consumer retry log leaked dependency detail or omitted safe code: %s", output.String())
		}
	})
}

func TestRegisterConsumerRetriesWithoutMetrics(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		lifecycle := new(captureLifecycle)
		var attempts atomic.Int32
		RegisterConsumer(lifecycle, "test", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, func(ctx context.Context) error {
			if attempts.Add(1) == 1 {
				return errors.New("subscription unavailable")
			}
			<-ctx.Done()
			return ctx.Err()
		})
		if err := lifecycle.hook.OnStart(t.Context()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := attempts.Load(); got != 2 {
			t.Fatalf("consumer attempts = %d, want retry without metrics", got)
		}
		if err := lifecycle.hook.OnStop(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRegisterConsumerRecoversPanickedSubscriptionAndRetries(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		lifecycle := new(captureLifecycle)
		var attempts atomic.Int32
		var output bytes.Buffer
		RegisterConsumer(lifecycle, "test", slog.New(slog.NewJSONHandler(&output, nil)), nil, func(ctx context.Context) error {
			if attempts.Add(1) == 1 {
				panic("token=secret-value")
			}
			<-ctx.Done()
			return ctx.Err()
		})
		if err := lifecycle.hook.OnStart(t.Context()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := attempts.Load(); got != 2 {
			t.Fatalf("consumer attempts = %d, want retry after panic", got)
		}
		if err := lifecycle.hook.OnStop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(output.Bytes(), []byte("secret-value")) || !bytes.Contains(output.Bytes(), []byte("event_consumer_panicked")) {
			t.Fatalf("consumer panic log leaked value or omitted safe code: %s", output.String())
		}
	})
}

func TestRegisterConsumerStopsWithoutRetryingAfterShutdown(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		lifecycle := new(captureLifecycle)
		var attempts atomic.Int32
		called := make(chan struct{})
		metrics := observability.NewMetrics(config.Config{Observability: config.Observability{MetricsEnabled: true}}, nil, nil)
		RegisterConsumer(lifecycle, "test", slog.New(slog.NewTextHandler(io.Discard, nil)), metrics, func(context.Context) error {
			attempts.Add(1)
			close(called)
			return errors.New("subscription unavailable")
		})
		if err := lifecycle.hook.OnStart(context.Background()); err != nil {
			t.Fatal(err)
		}
		<-called
		synctest.Wait()
		if err := lifecycle.hook.OnStop(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		if got := attempts.Load(); got != 1 {
			t.Fatalf("consumer attempts after shutdown = %d, want 1", got)
		}
		recorder := httptest.NewRecorder()
		metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if strings.Contains(recorder.Body.String(), `event_consumer_restarts_total{consumer="test"}`) {
			t.Fatalf("shutdown without a second attempt counted a restart: %s", recorder.Body.String())
		}
	})
}

func TestConsumerRetryJitterStaysWithinCappedBackoff(t *testing.T) {
	t.Parallel()
	for _, backoff := range []time.Duration{consumerRetryMin, 8 * time.Second, consumerRetryMax} {
		for range 100 {
			delay := jitterConsumerDelay(backoff)
			if delay < backoff/2 || delay > backoff {
				t.Fatalf("jitter delay = %s outside [%s, %s]", delay, backoff/2, backoff)
			}
		}
	}
}
