package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	redisrate "github.com/go-redis/redis_rate/v10"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/redis/go-redis/v9"
)

type Result struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration
}
type Limiter struct {
	enabled   bool
	failOpen  bool
	keyPrefix string
	backend   *redisrate.Limiter
	metrics   *observability.Metrics
}

func New(client *redis.Client, cfg config.Config, metrics *observability.Metrics) *Limiter {
	limiter := &Limiter{enabled: cfg.RateLimit.Enabled, failOpen: cfg.RateLimit.FailOpen, keyPrefix: cfg.RedisKeyPrefix(), metrics: metrics}
	if client != nil {
		limiter.backend = redisrate.NewLimiter(client)
	}
	return limiter
}
func (l *Limiter) Enabled() bool  { return l.enabled }
func (l *Limiter) FailOpen() bool { return l.failOpen }
func (l *Limiter) Allow(ctx context.Context, key string, rule config.RateLimitRule) (Result, error) {
	if !l.enabled {
		return Result{Allowed: true}, nil
	}
	if l.backend == nil {
		return Result{}, errors.New("redis rate limiter is unavailable")
	}
	if key == "" || len(key) > 4096 {
		return Result{}, errors.New("rate limit key must contain between 1 and 4096 bytes")
	}
	digest := sha256.Sum256([]byte(key))
	redisKey := l.keyPrefix + "rate-limit:" + hex.EncodeToString(digest[:])
	started := time.Now()
	result, err := l.backend.Allow(ctx, redisKey, redisrate.Limit{Rate: rule.Rate, Burst: rule.Burst, Period: rule.Period})
	status := "success"
	if err != nil {
		status = "error"
	}
	if l.metrics != nil {
		l.metrics.ObserveInfrastructure("rate_limit", "redis", "allow", status, started)
	}
	if err != nil {
		return Result{}, fmt.Errorf("check redis rate limit: %w", err)
	}
	return Result{Allowed: result.Allowed > 0, Limit: rule.Burst, Remaining: result.Remaining, RetryAfter: result.RetryAfter}, nil
}
