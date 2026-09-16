package ratelimit

import (
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redisrate "github.com/go-redis/redis_rate/v10"
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
	limiter := New(client, config.Config{App: config.App{Name: "service-a"}, RateLimit: config.RateLimit{Enabled: true}}, nil)
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
	if len(keys) != 1 || !strings.HasPrefix(keys[0], "rate:development:service-a:rate-limit:") || strings.Contains(keys[0], ":test") {
		t.Fatalf("rate-limit state is not scoped and pseudonymized: %v", keys)
	}
}

func TestLimiter_DefaultPrefixIsolatedBetweenEnvironments(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	rule := config.RateLimitRule{Rate: 1, Burst: 1, Period: time.Minute}
	development := New(client, config.Config{App: config.App{Name: "service-a", Env: "development"}, RateLimit: config.RateLimit{Enabled: true}}, nil)
	production := New(client, config.Config{App: config.App{Name: "service-a", Env: "production"}, RateLimit: config.RateLimit{Enabled: true}}, nil)
	if result, err := development.Allow(t.Context(), "same-client", rule); err != nil || !result.Allowed {
		t.Fatalf("development Allow() = %+v, %v", result, err)
	}
	if result, err := production.Allow(t.Context(), "same-client", rule); err != nil || !result.Allowed {
		t.Fatalf("production Allow() = %+v, %v", result, err)
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
	first := New(client, config.Config{Redis: config.Redis{KeyPrefix: "first:"}, RateLimit: config.RateLimit{Enabled: true}}, nil)
	second := New(client, config.Config{Redis: config.Redis{KeyPrefix: "second:"}, RateLimit: config.RateLimit{Enabled: true}}, nil)
	if result, err := first.Allow(t.Context(), "rate:ip:127.0.0.1", rule); err != nil || !result.Allowed {
		t.Fatalf("first Allow() = %+v, %v", result, err)
	}
	if result, err := second.Allow(t.Context(), "rate:ip:127.0.0.1", rule); err != nil || !result.Allowed {
		t.Fatalf("second Allow() = %+v, %v", result, err)
	}
}

func TestLimiter_Disabled(t *testing.T) {
	t.Parallel()
	limiter := New(nil, config.Config{}, nil)
	result, err := limiter.Allow(t.Context(), "test", config.RateLimitRule{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Allowed {
		t.Fatal("disabled limiter denied request")
	}
}

func TestLimiter_RejectsUnboundedKey(t *testing.T) {
	t.Parallel()
	limiter := &Limiter{enabled: true, backend: new(redisrate.Limiter)}
	_, err := limiter.Allow(t.Context(), strings.Repeat("x", 4097), config.RateLimitRule{Rate: 1, Burst: 1, Period: time.Minute})
	if err == nil {
		t.Fatal("Allow() accepted an unbounded key")
	}
}
