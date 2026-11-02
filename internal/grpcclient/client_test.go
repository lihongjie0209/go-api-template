package grpcclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/sony/gobreaker/v2"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/fx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type tracingClientLifecycle struct{ hooks []fx.Hook }

func (l *tracingClientLifecycle) Append(hook fx.Hook) { l.hooks = append(l.hooks, hook) }

func TestGRPCClientTracesAfterFxStart(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	statusServer := health.NewServer()
	statusServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, statusServer)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	lifecycle := new(tracingClientLifecycle)
	_, err = observability.NewTracing(lifecycle, config.Config{Observability: config.Observability{TracingEnabled: true, TracingEndpoint: collector.URL, TracingSampleRatio: 1}})
	require.NoError(t, err)
	connection, err := Dial(Config{
		Name: "inventory", Target: listener.Addr().String(), Timeout: time.Second,
		Retry: config.Retry{MaxAttempts: 1, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond},
	})
	require.NoError(t, err)
	defer func() { _ = connection.Close() }()
	require.Len(t, lifecycle.hooks, 1)
	started := false
	defer func() {
		if started {
			_ = lifecycle.hooks[0].OnStop(context.Background())
		}
	}()
	require.NoError(t, lifecycle.hooks[0].OnStart(t.Context()))
	started = true
	provider, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	require.True(t, ok)
	recorder := tracetest.NewSpanRecorder()
	provider.RegisterSpanProcessor(recorder)
	response, err := healthpb.NewHealthClient(connection).Check(t.Context(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	require.Equal(t, healthpb.HealthCheckResponse_SERVING, response.GetStatus())
	require.NotEmpty(t, recorder.Ended(), "gRPC client captured an obsolete tracer provider")
}

func TestMetadataStreamInterceptorPropagatesAuthenticationAndCorrelation(t *testing.T) {
	t.Parallel()

	ctx := WithRequestID(context.Background(), "request-1")
	ctx = WithIdempotencyKey(ctx, "operation-1")
	interceptor := metadataStreamInterceptor("", "test-psk")
	var captured metadata.MD

	_, err := interceptor(ctx, &grpc.StreamDesc{}, nil, "/example.v1.Service/Stream", func(streamCtx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
		captured, _ = metadata.FromOutgoingContext(streamCtx)
		return nil, nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"PSK test-psk"}, captured.Get("authorization"))
	require.Equal(t, []string{"request-1"}, captured.Get("x-request-id"))
	require.Equal(t, []string{"operation-1"}, captured.Get("idempotency-key"))
}

func TestRetryInterceptorRetriesOnlyReviewedCalls(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		method       string
		patterns     []string
		withKey      bool
		wantAttempts int32
	}{
		{name: "unreviewed mutation", method: "/orders.v1.OrderService/Create", wantAttempts: 1},
		{name: "configured query", method: "/orders.v1.OrderService/Get", patterns: []string{"/orders.v1.OrderService/Get"}, wantAttempts: 3},
		{name: "idempotency protected mutation", method: "/orders.v1.OrderService/Create", withKey: true, wantAttempts: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			if test.withKey {
				ctx = WithIdempotencyKey(ctx, "operation-1")
			}
			var attempts atomic.Int32
			interceptor := retryInterceptor(config.Retry{MaxAttempts: 3, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond, Methods: test.patterns})
			err := interceptor(ctx, test.method, nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
				if attempts.Add(1) < 3 {
					return status.Error(codes.Unavailable, "temporarily unavailable")
				}
				return nil
			})
			if test.wantAttempts == 1 {
				require.Equal(t, codes.Unavailable, status.Code(err))
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, test.wantAttempts, attempts.Load())
		})
	}
}

func TestBreakerInterceptorOpensAfterThreshold(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	interceptor := breakerInterceptor("inventory", config.Breaker{Enabled: true, FailureThreshold: 1, OpenTimeout: time.Minute})
	invoker := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		attempts.Add(1)
		return status.Error(codes.Unavailable, "unavailable")
	}
	require.Equal(t, codes.Unavailable, status.Code(interceptor(t.Context(), "/inventory.v1.Inventory/Get", nil, nil, nil, invoker)))
	err := interceptor(t.Context(), "/inventory.v1.Inventory/Get", nil, nil, nil, invoker)
	require.True(t, errors.Is(err, gobreaker.ErrOpenState), "error = %v", err)
	require.Equal(t, int32(1), attempts.Load())
}

func TestMetadataInterceptorReplacesCallerAuthentication(t *testing.T) {
	t.Parallel()
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer caller-controlled",
		"x-custom", "preserved",
	))
	interceptor := metadataInterceptor("trusted-token", "")
	var captured metadata.MD
	err := interceptor(ctx, "/example.v1.Service/Get", nil, nil, nil, func(callCtx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		captured, _ = metadata.FromOutgoingContext(callCtx)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"Bearer trusted-token"}, captured.Get("authorization"))
	require.Equal(t, []string{"preserved"}, captured.Get("x-custom"))
}

func TestOutgoingCorrelationMetadataComesOnlyFromValidatedContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		requestID     string
		idempotencyID string
		invalidKey    bool
	}{
		{name: "caller metadata without context"},
		{name: "invalid context key does not restore caller metadata", invalidKey: true},
		{name: "validated context overrides caller metadata", requestID: "trusted-request", idempotencyID: "trusted-operation"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs(
				"x-request-id", "forged-request",
				"idempotency-key", "forged-operation",
				"x-custom", "preserved",
			))
			if test.requestID != "" {
				ctx = WithRequestID(ctx, test.requestID)
				ctx = WithIdempotencyKey(ctx, test.idempotencyID)
			} else if test.invalidKey {
				ctx = WithIdempotencyKey(ctx, "invalid\nkey")
			}
			outgoing, ok := metadata.FromOutgoingContext(withOutgoingMetadata(ctx, "", ""))
			require.True(t, ok)
			require.Equal(t, []string{"preserved"}, outgoing.Get("x-custom"))
			if test.requestID == "" {
				require.Empty(t, outgoing.Get("x-request-id"))
				require.Empty(t, outgoing.Get("idempotency-key"))
			} else {
				require.Equal(t, []string{test.requestID}, outgoing.Get("x-request-id"))
				require.Equal(t, []string{test.idempotencyID}, outgoing.Get("idempotency-key"))
			}
		})
	}
}

func TestTimeoutInterceptorCapsLongerCallerDeadline(t *testing.T) {
	t.Parallel()
	caller, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := time.Now()
	err := timeoutInterceptor(20*time.Millisecond)(caller, "/example.v1.Service/Get", nil, nil, nil, func(callCtx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		<-callCtx.Done()
		return callCtx.Err()
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), 250*time.Millisecond)
}

func TestTimeoutInterceptorRejectsLateSuccess(t *testing.T) {
	t.Parallel()
	err := timeoutInterceptor(10*time.Millisecond)(t.Context(), "/example.v1.Service/Get", nil, nil, nil, func(callCtx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		<-callCtx.Done()
		return nil
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err = timeoutInterceptor(time.Second)(ctx, "/example.v1.Service/Get", nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		cancel()
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestTimeoutInterceptorDoesNotStartCanceledCall(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	err := timeoutInterceptor(time.Second)(ctx, "/example.v1.Service/Get", nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, called)
}

func TestBreakerInterceptorCountsLateDeadlineAsFailure(t *testing.T) {
	t.Parallel()
	interceptor := breakerInterceptor("inventory", config.Breaker{Enabled: true, FailureThreshold: 1, OpenTimeout: time.Minute})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	err := interceptor(ctx, "/inventory.v1.Inventory/Get", nil, nil, nil, func(callCtx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		<-callCtx.Done()
		return nil
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	called := false
	err = interceptor(t.Context(), "/inventory.v1.Inventory/Get", nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, gobreaker.ErrOpenState)
	require.False(t, called)
}

func TestMetricsInterceptorRecordsLateCancellation(t *testing.T) {
	t.Parallel()
	metrics := observability.NewMetrics(config.Config{Observability: config.Observability{MetricsEnabled: true}}, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := metricsInterceptor("inventory", metrics)(ctx, "/inventory.v1.Inventory/Get", nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		cancel()
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	require.Contains(t, recorder.Body.String(), `outbound_requests_total{client="inventory",protocol="grpc",status="Canceled"} 1`)
}

func TestDialRejectsUnsafeDirectConfiguration(t *testing.T) {
	t.Parallel()
	valid := func() Config {
		return Config{
			Name:    "inventory",
			Target:  "dns:///inventory:9090",
			Timeout: time.Second,
			Retry: config.Retry{
				MaxAttempts:    1,
				InitialBackoff: 10 * time.Millisecond,
				MaxBackoff:     10 * time.Millisecond,
			},
		}
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "missing name", mutate: func(cfg *Config) { cfg.Name = "" }},
		{name: "target whitespace", mutate: func(cfg *Config) { cfg.Target = "dns:///inventory service:9090" }},
		{name: "unbounded timeout", mutate: func(cfg *Config) { cfg.Timeout = 6 * time.Minute }},
		{name: "unbounded attempts", mutate: func(cfg *Config) { cfg.Retry.MaxAttempts = 6 }},
		{name: "invalid method", mutate: func(cfg *Config) { cfg.Retry.Methods = []string{"invalid"} }},
		{name: "disabled TLS with settings", mutate: func(cfg *Config) { cfg.TLS.ServerName = "inventory" }},
		{name: "incomplete mTLS", mutate: func(cfg *Config) { cfg.TLS.Enabled = true; cfg.TLS.CertFile = "client.pem" }},
		{name: "TLS with plaintext opt-in", mutate: func(cfg *Config) { cfg.TLS.Enabled = true; cfg.TLS.AllowInsecureToken = true }},
		{name: "dual credentials", mutate: func(cfg *Config) { cfg.TLS.AllowInsecureToken = true; cfg.Token = "token"; cfg.PSK = "psk" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid()
			test.mutate(&cfg)
			connection, err := Dial(cfg)
			if connection != nil {
				_ = connection.Close()
			}
			require.Error(t, err)
		})
	}
}
