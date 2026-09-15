package ratelimit

import (
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/redis/go-redis/v9"
)

func TestLimiter_Allow(t *testing.T) {
	t.Parallel()
	server, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer func() { _ = client.Close() }()
	limiter := New(client, config.Config{App: config.App{Name: "service-a"}, RateLimit: config.RateLimit{Enabled: true}})
	rule := config.RateLimitRule{Rate: 1, Burst: 1, Period: time.Minute}
	first, err := limiter.Allow(t.Context(), "test", rule)
	if err != nil {
		t.Fatalf("first Allow() error = %v", err)
	}
	if !first.Allowed {
		t.Fatal("first Allow() allowed = false")
	}
	second, err := limiter.Allow(t.Context(), "test", rule)
	if err != nil {
		t.Fatalf("second Allow() error = %v", err)
	}
	if second.Allowed {
		t.Fatal("second Allow() allowed = true")
	}
	if second.RetryAfter <= 0 {
		t.Fatalf("RetryAfter = %v", second.RetryAfter)
	}
	keys := server.Keys()
	if len(keys) != 1 || !strings.Contains(keys[0], "service-a:test") {
		t.Fatalf("rate-limit state is not scoped by service name: %v", keys)
	}
}

func TestLimiter_ExplicitRedisPrefixIsolatedBetweenServices(t *testing.T) {
	t.Parallel()
	server, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	rule := config.RateLimitRule{Rate: 1, Burst: 1, Period: time.Minute}
	first := New(client, config.Config{Redis: config.Redis{KeyPrefix: "first:"}, RateLimit: config.RateLimit{Enabled: true}})
	second := New(client, config.Config{Redis: config.Redis{KeyPrefix: "second:"}, RateLimit: config.RateLimit{Enabled: true}})
	if result, err := first.Allow(t.Context(), "rate:ip:127.0.0.1", rule); err != nil || !result.Allowed {
		t.Fatalf("first Allow() = %+v, %v", result, err)
	}
	if result, err := second.Allow(t.Context(), "rate:ip:127.0.0.1", rule); err != nil || !result.Allowed {
		t.Fatalf("second Allow() = %+v, %v", result, err)
	}
}

func TestLimiter_Disabled(t *testing.T) {
	t.Parallel()
	limiter := New(nil, config.Config{})
	result, err := limiter.Allow(t.Context(), "test", config.RateLimitRule{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Allowed {
		t.Fatal("disabled limiter denied request")
	}
}
