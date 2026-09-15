package cache

import (
	platformcache "github.com/lihongjie0209/microservice-platform-go/cache"
	"github.com/redis/go-redis/v9"
)

type RedisStore = platformcache.RedisStore

type StoreOption = platformcache.Option

func WithKeyPrefix(prefix string) StoreOption { return platformcache.WithKeyPrefix(prefix) }
func WithMaxValueBytes(size int) StoreOption  { return platformcache.WithMaxValueBytes(size) }

func NewRedisStore(client redis.UniversalClient, options ...StoreOption) *RedisStore {
	return platformcache.NewRedisStore(client, options...)
}

var _ Store = (*RedisStore)(nil)
