package cache

import (
	"context"
	"fmt"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/microservice-platform-go/distlock"
	"github.com/redis/go-redis/v9"
)

func Open(ctx context.Context, cfg config.Redis) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{Addr: cfg.Address, Username: cfg.Username, Password: cfg.Password, DB: cfg.DB, DialTimeout: cfg.DialTimeout, ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}

const lockKeyPrefix = "lock:"

type lockerOptions struct{ prefix string }
type LockerOption func(*lockerOptions)

func WithLockKeyPrefix(prefix string) LockerOption {
	return func(options *lockerOptions) { options.prefix = prefix }
}

func NewLocker(client redis.UniversalClient, options ...LockerOption) Locker {
	settings := lockerOptions{prefix: lockKeyPrefix}
	for _, option := range options {
		option(&settings)
	}
	return distlock.NewRedisLockerWithPrefix(client, settings.prefix)
}
