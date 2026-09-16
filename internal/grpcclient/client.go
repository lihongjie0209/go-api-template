package grpcclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	rand "math/rand/v2"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	"github.com/sony/gobreaker/v2"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/balancer/roundrobin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type Config struct {
	Target  string
	Timeout time.Duration
	Token   string
	PSK     string
	TLS     TLSConfig
	Name    string
	Retry   config.Retry
	Breaker config.Breaker
	Metrics *observability.Metrics
}
type TLSConfig struct {
	Enabled            bool
	ServerName         string
	CAFile             string
	CertFile           string
	KeyFile            string
	AllowInsecureToken bool
}

var validClientName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

func Dial(cfg Config) (*grpc.ClientConn, error) {
	if !validClientName.MatchString(cfg.Name) {
		return nil, errors.New("grpc client name must be a bounded lowercase identifier")
	}
	if cfg.Target == "" || len(cfg.Target) > 2048 {
		return nil, errors.New("grpc client target is required and must not exceed 2048 bytes")
	}
	if strings.IndexFunc(cfg.Target, func(character rune) bool { return character <= ' ' || character == 0x7f }) >= 0 {
		return nil, errors.New("grpc client target must not contain whitespace or control characters")
	}
	if cfg.Timeout < 10*time.Millisecond || cfg.Timeout > 5*time.Minute {
		return nil, errors.New("grpc client timeout must be between 10ms and 5m")
	}
	if cfg.Retry.MaxAttempts < 1 || cfg.Retry.MaxAttempts > 5 || cfg.Retry.InitialBackoff < 10*time.Millisecond || cfg.Retry.InitialBackoff > time.Minute || cfg.Retry.MaxBackoff < cfg.Retry.InitialBackoff || cfg.Retry.MaxBackoff > time.Minute || len(cfg.Retry.Methods) > 100 {
		return nil, errors.New("grpc client retry policy is invalid")
	}
	for _, pattern := range cfg.Retry.Methods {
		if len(pattern) > 256 || !strings.HasPrefix(pattern, "/") || strings.Count(pattern, "/") != 2 {
			return nil, errors.New("grpc client retry method pattern is invalid")
		}
		if _, err := path.Match(pattern, "/validation/target"); err != nil {
			return nil, fmt.Errorf("grpc client retry method pattern is invalid: %w", err)
		}
	}
	if cfg.Breaker.Enabled && (cfg.Breaker.FailureThreshold == 0 || cfg.Breaker.FailureThreshold > 10000 || cfg.Breaker.OpenTimeout < time.Second || cfg.Breaker.OpenTimeout > time.Hour) {
		return nil, errors.New("grpc client breaker policy is invalid")
	}
	if len(cfg.Token) > 8192 || len(cfg.PSK) > 8192 {
		return nil, errors.New("grpc client credential must not exceed 8192 bytes")
	}
	transport, err := transportCredentials(cfg.TLS)
	if err != nil {
		return nil, err
	}
	if cfg.Token != "" && !cfg.TLS.Enabled && !cfg.TLS.AllowInsecureToken {
		return nil, errors.New("refusing to send grpc bearer token without TLS")
	}
	if cfg.PSK != "" && cfg.Token != "" {
		return nil, errors.New("grpc client bearer token and PSK are mutually exclusive")
	}
	if cfg.PSK != "" && !cfg.TLS.Enabled && !cfg.TLS.AllowInsecureToken {
		return nil, errors.New("refusing to send grpc PSK without TLS")
	}
	interceptors := []grpc.UnaryClientInterceptor{timeoutInterceptor(cfg.Timeout), metadataInterceptor(cfg.Token, cfg.PSK)}
	if cfg.Metrics != nil {
		interceptors = append(interceptors, metricsInterceptor(cfg.Name, cfg.Metrics))
	}
	if cfg.Breaker.Enabled {
		interceptors = append(interceptors, breakerInterceptor(cfg.Name, cfg.Breaker))
	}
	if cfg.Retry.MaxAttempts > 1 {
		interceptors = append(interceptors, retryInterceptor(cfg.Retry))
	}
	options := []grpc.DialOption{
		grpc.WithTransportCredentials(transport),
		grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}]}`),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithChainUnaryInterceptor(interceptors...),
		grpc.WithChainStreamInterceptor(metadataStreamInterceptor(cfg.Token, cfg.PSK)),
	}
	return grpc.NewClient(cfg.Target, options...)
}

func retryInterceptor(cfg config.Retry) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, connection *grpc.ClientConn, invoker grpc.UnaryInvoker, options ...grpc.CallOption) error {
		if !retryGRPCMethod(ctx, method, cfg.Methods) {
			return invoker(ctx, method, req, reply, connection, options...)
		}
		var err error
		for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
			err = invoker(ctx, method, req, reply, connection, options...)
			code := status.Code(err)
			if code != codes.Unavailable && code != codes.ResourceExhausted {
				return err
			}
			if attempt < cfg.MaxAttempts {
				if waitErr := waitRetry(ctx, cfg, attempt); waitErr != nil {
					return waitErr
				}
			}
		}
		return err
	}
}

func retryGRPCMethod(ctx context.Context, method string, patterns []string) bool {
	if _, ok := idempotency.FromContext(ctx); ok {
		return true
	}
	for _, pattern := range patterns {
		if matched, _ := path.Match(pattern, method); matched {
			return true
		}
	}
	return false
}

func waitRetry(ctx context.Context, cfg config.Retry, attempt int) error {
	delay := cfg.InitialBackoff << (attempt - 1)
	if delay > cfg.MaxBackoff {
		delay = cfg.MaxBackoff
	}
	half := delay / 2
	if spread := delay - half; spread > 0 {
		delay = half + time.Duration(rand.Int64N(int64(spread)+1))
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func breakerInterceptor(name string, cfg config.Breaker) grpc.UnaryClientInterceptor {
	breaker := gobreaker.NewCircuitBreaker[any](gobreaker.Settings{
		Name: name, Timeout: cfg.OpenTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool { return counts.ConsecutiveFailures >= cfg.FailureThreshold },
		IsExcluded: func(err error) bool {
			code := status.Code(err)
			return code != codes.Unavailable && code != codes.ResourceExhausted && code != codes.DeadlineExceeded
		},
	})
	return func(ctx context.Context, method string, req, reply any, connection *grpc.ClientConn, invoker grpc.UnaryInvoker, options ...grpc.CallOption) error {
		_, err := breaker.Execute(func() (any, error) { return nil, invoker(ctx, method, req, reply, connection, options...) })
		return err
	}
}

func metricsInterceptor(name string, metrics *observability.Metrics) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, connection *grpc.ClientConn, invoker grpc.UnaryInvoker, options ...grpc.CallOption) error {
		started := time.Now()
		err := invoker(ctx, method, req, reply, connection, options...)
		if metrics.Enabled() {
			metrics.OutboundRequests.WithLabelValues("grpc", name, status.Code(err).String()).Inc()
			metrics.OutboundDuration.WithLabelValues("grpc", name).Observe(time.Since(started).Seconds())
		}
		return err
	}
}

func timeoutInterceptor(timeout time.Duration) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, connection *grpc.ClientConn, invoker grpc.UnaryInvoker, options ...grpc.CallOption) error {
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return invoker(callCtx, method, req, reply, connection, options...)
	}
}
func metadataInterceptor(token, psk string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, connection *grpc.ClientConn, invoker grpc.UnaryInvoker, options ...grpc.CallOption) error {
		ctx = withOutgoingMetadata(ctx, token, psk)
		return invoker(ctx, method, req, reply, connection, options...)
	}
}

func metadataStreamInterceptor(token, psk string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, descriptor *grpc.StreamDesc, connection *grpc.ClientConn, method string, streamer grpc.Streamer, options ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(withOutgoingMetadata(ctx, token, psk), descriptor, connection, method, options...)
	}
}

func withOutgoingMetadata(ctx context.Context, token, psk string) context.Context {
	values, _ := metadata.FromOutgoingContext(ctx)
	values = values.Copy()
	if token != "" {
		values.Set("authorization", "Bearer "+token)
	} else if psk != "" {
		values.Set("authorization", "PSK "+psk)
	}
	if requestID, ok := RequestIDFromContext(ctx); ok {
		values.Set("x-request-id", requestID)
	}
	if key, ok := idempotency.FromContext(ctx); ok {
		values.Set("idempotency-key", key)
	}
	if len(values) == 0 {
		return ctx
	}
	return metadata.NewOutgoingContext(ctx, values)
}

func WithRequestID(ctx context.Context, id string) context.Context {
	return requestid.WithContext(ctx, id)
}
func RequestIDFromContext(ctx context.Context) (string, bool) {
	return requestid.FromContext(ctx)
}
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	return idempotency.WithContext(ctx, key)
}

func transportCredentials(cfg TLSConfig) (credentials.TransportCredentials, error) {
	if len(cfg.ServerName) > 253 || len(cfg.CAFile) > 4096 || len(cfg.CertFile) > 4096 || len(cfg.KeyFile) > 4096 {
		return nil, errors.New("grpc TLS settings exceed their bounds")
	}
	if !cfg.Enabled {
		if cfg.ServerName != "" || cfg.CAFile != "" || cfg.CertFile != "" || cfg.KeyFile != "" {
			return nil, errors.New("grpc TLS settings require TLS to be enabled")
		}
		return insecure.NewCredentials(), nil
	}
	if cfg.AllowInsecureToken {
		return nil, errors.New("grpc TLS and plaintext credential opt-in are mutually exclusive")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.ServerName}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read grpc CA: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system certificate pool: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("parse grpc CA")
		}
		tlsConfig.RootCAs = pool
	}
	if cfg.CertFile != "" || cfg.KeyFile != "" {
		if cfg.CertFile == "" || cfg.KeyFile == "" {
			return nil, errors.New("grpc client certificate and key must be configured together")
		}
		certificate, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load grpc client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	return credentials.NewTLS(tlsConfig), nil
}
