package grpcclient

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/sony/gobreaker/v2"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

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
