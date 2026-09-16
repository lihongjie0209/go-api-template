package outbound

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	"github.com/sony/gobreaker/v2"
)

func validHTTPUpstream(baseURL string) config.HTTPUpstream {
	return config.HTTPUpstream{
		BaseURL: baseURL,
		Timeout: time.Second,
		Retry: config.Retry{
			MaxAttempts:    1,
			InitialBackoff: 10 * time.Millisecond,
			MaxBackoff:     10 * time.Millisecond,
		},
	}
}

func TestHTTPClient_ConfiguredAuthenticationReplacesCallerHeader(t *testing.T) {
	t.Parallel()
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg := validHTTPUpstream(server.URL)
	cfg.Auth = config.ClientAuth{Type: "psk", Token: "trusted-secret"}
	cfg.TLS.AllowInsecure = true
	client, err := NewHTTPClient("test", cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer caller-controlled")
	response, err := client.Do(t.Context(), http.MethodGet, "/auth", nil, headers)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if authorization != "PSK trusted-secret" {
		t.Fatalf("Authorization = %q", authorization)
	}
}

func TestHTTPClient_BreakerOpensAfterThreshold(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	cfg := validHTTPUpstream(server.URL)
	cfg.Breaker = config.Breaker{Enabled: true, FailureThreshold: 1, OpenTimeout: time.Minute}
	client, err := NewHTTPClient("test", cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(t.Context(), http.MethodGet, "/failure", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	response, err = client.Do(t.Context(), http.MethodGet, "/failure", nil, nil)
	if response != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(err, gobreaker.ErrOpenState) {
		t.Fatalf("Do() error = %v, want open breaker", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestHTTPClient_RetryPolicy(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		withKey      bool
		wantAttempts int32
	}{
		{name: "POST is not retried by default", wantAttempts: 1},
		{name: "idempotent POST is retried", withKey: true, wantAttempts: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				writer.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()
			cfg := validHTTPUpstream(server.URL)
			cfg.Retry = config.Retry{MaxAttempts: 3, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond}
			client, err := NewHTTPClient("test", cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if test.withKey {
				ctx = idempotency.WithContext(ctx, "request-0001")
			}
			response, err := client.Do(ctx, http.MethodPost, "/test", []byte(`{}`), nil)
			if err != nil {
				t.Fatal(err)
			}
			if response != nil {
				_ = response.Body.Close()
			}
			if got := attempts.Load(); got != test.wantAttempts {
				t.Fatalf("attempts = %d, want %d", got, test.wantAttempts)
			}
		})
	}
}

func TestHTTPClient_RejectsPlaintextCredentials(t *testing.T) {
	t.Parallel()
	cfg := validHTTPUpstream("http://example.com")
	cfg.Auth = config.ClientAuth{Type: "psk", Token: "secret"}
	_, err := NewHTTPClient("test", cfg, nil)
	if err == nil {
		t.Fatal("NewHTTPClient() error = nil")
	}
}

func TestHTTPClient_PropagatesCorrelationHeaders(t *testing.T) {
	t.Parallel()
	var requestIDHeader, idempotencyHeader string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestIDHeader = request.Header.Get("X-Request-ID")
		idempotencyHeader = request.Header.Get("Idempotency-Key")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := NewHTTPClient("test", validHTTPUpstream(server.URL), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := requestid.WithContext(context.Background(), "request-1")
	ctx = idempotency.WithContext(ctx, "operation-1")
	response, err := client.Do(ctx, http.MethodPost, "/test", []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if requestIDHeader != "request-1" || idempotencyHeader != "operation-1" {
		t.Fatalf("headers request_id=%q idempotency=%q", requestIDHeader, idempotencyHeader)
	}
}

func TestHTTPClient_RejectsTLSConfigWithPlainHTTPURL(t *testing.T) {
	t.Parallel()
	cfg := validHTTPUpstream("http://example.com")
	cfg.TLS = config.ClientTLS{Enabled: true}
	_, err := NewHTTPClient("test", cfg, nil)
	if err == nil {
		t.Fatal("NewHTTPClient() error = nil")
	}
}

func TestHTTPClient_AllowsExplicitPlaintextCredentialsForDevelopment(t *testing.T) {
	t.Parallel()
	cfg := validHTTPUpstream("http://example.com")
	cfg.Auth = config.ClientAuth{Type: "psk", Token: "secret"}
	cfg.TLS = config.ClientTLS{AllowInsecure: true}
	_, err := NewHTTPClient("test", cfg, nil)
	if err != nil {
		t.Fatalf("NewHTTPClient() error = %v", err)
	}
}

func TestHTTPClient_DoesNotFollowUpstreamRedirects(t *testing.T) {
	t.Parallel()
	var redirectTargetCalled atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectTargetCalled.Store(true) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", target.URL)
		writer.WriteHeader(http.StatusFound)
	}))
	defer source.Close()
	client, err := NewHTTPClient("test", validHTTPUpstream(source.URL), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(t.Context(), http.MethodGet, "/test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusFound || redirectTargetCalled.Load() {
		t.Fatalf("status=%d redirect_target_called=%t", response.StatusCode, redirectTargetCalled.Load())
	}
}

func TestHTTPClient_RejectsUnsafeBaseURL(t *testing.T) {
	t.Parallel()
	for _, baseURL := range []string{
		"ftp://example.com",
		"https://user:secret@example.com",
		"https://example.com?token=secret",
		"https://example.com#fragment",
	} {
		baseURL := baseURL
		t.Run(baseURL, func(t *testing.T) {
			t.Parallel()
			_, err := NewHTTPClient("test", validHTTPUpstream(baseURL), nil)
			if err == nil {
				t.Fatal("NewHTTPClient() error = nil")
			}
			if got := err.Error(); got == "" || containsSensitiveURL(got) {
				t.Fatalf("NewHTTPClient() error leaked URL credentials: %q", got)
			}
		})
	}
}

func TestHTTPClient_RejectsUnsafeDirectClientName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "Uppercase", "contains space", strings.Repeat("x", 64)} {
		if _, err := NewHTTPClient(name, validHTTPUpstream("https://example.com"), nil); err == nil {
			t.Fatalf("NewHTTPClient(%q) error = nil", name)
		}
	}
}

func containsSensitiveURL(message string) bool {
	return strings.Contains(message, "user:secret") || strings.Contains(message, "token=secret")
}

func TestHTTPClient_ConfiguredTimeoutCapsLongerCallerDeadline(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	cfg := validHTTPUpstream(server.URL)
	cfg.Timeout = 20 * time.Millisecond
	client, err := NewHTTPClient("test", cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	caller, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := time.Now()
	response, err := client.Do(caller, http.MethodGet, "/slow", nil, nil)
	if response != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Do() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("Do() elapsed = %v, configured timeout was not enforced", elapsed)
	}
}

func TestHTTPClient_ResponseBodyRemainsReadableUntilClosed(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("complete response"))
	}))
	defer server.Close()
	client, err := NewHTTPClient("test", validHTTPUpstream(server.URL), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(t.Context(), http.MethodGet, "/response", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != "complete response" {
		t.Fatalf("body = %q", body)
	}
}

func TestHTTPClient_RejectsIncompleteOrDisabledTLSConfiguration(t *testing.T) {
	t.Parallel()
	for _, tlsConfig := range []config.ClientTLS{
		{CAFile: "ca.pem"},
		{Enabled: true, CertFile: "client.pem"},
		{Enabled: true, AllowInsecure: true},
	} {
		cfg := validHTTPUpstream("https://example.com")
		cfg.TLS = tlsConfig
		if _, err := NewHTTPClient("test", cfg, nil); err == nil {
			t.Fatalf("NewHTTPClient(%+v) error = nil", tlsConfig)
		}
	}
}
