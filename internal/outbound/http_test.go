package outbound

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
)

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
			client, err := NewHTTPClient("test", config.HTTPUpstream{BaseURL: server.URL, Timeout: time.Second, Retry: config.Retry{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}}, nil)
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
	_, err := NewHTTPClient("test", config.HTTPUpstream{BaseURL: "http://example.com", Timeout: time.Second, Auth: config.ClientAuth{Type: "psk", Token: "secret"}}, nil)
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
	client, err := NewHTTPClient("test", config.HTTPUpstream{BaseURL: server.URL, Timeout: time.Second, Retry: config.Retry{MaxAttempts: 1}}, nil)
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
	_, err := NewHTTPClient("test", config.HTTPUpstream{BaseURL: "http://example.com", Timeout: time.Second, TLS: config.ClientTLS{Enabled: true}}, nil)
	if err == nil {
		t.Fatal("NewHTTPClient() error = nil")
	}
}

func TestHTTPClient_AllowsExplicitPlaintextCredentialsForDevelopment(t *testing.T) {
	t.Parallel()
	_, err := NewHTTPClient("test", config.HTTPUpstream{BaseURL: "http://example.com", Timeout: time.Second, Auth: config.ClientAuth{Type: "psk", Token: "secret"}, TLS: config.ClientTLS{AllowInsecure: true}}, nil)
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
	client, err := NewHTTPClient("test", config.HTTPUpstream{BaseURL: source.URL, Timeout: time.Second, Retry: config.Retry{MaxAttempts: 1}}, nil)
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
