package cache

import (
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisStore_CRUDAndNamespace(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, WithKeyPrefix("orders:"))

	if err := store.Set(t.Context(), "item:1", []byte("value"), time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if !server.Exists("orders:item:1") {
		t.Fatal("Set() did not use the configured namespace")
	}
	value, err := store.Get(t.Context(), "item:1")
	if err != nil || string(value) != "value" {
		t.Fatalf("Get() = %q, %v, want value", value, err)
	}
	exists, err := store.Exists(t.Context(), "item:1")
	if err != nil || !exists {
		t.Fatalf("Exists() = %v, %v, want true", exists, err)
	}
	if err := store.Delete(t.Context(), "item:1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	_, err = store.Get(t.Context(), "item:1")
	if !errors.Is(err, ErrMiss) {
		t.Fatalf("Get() error = %v, want ErrMiss", err)
	}
}

func TestRedisStore_SetIfAbsent(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client)

	created, err := store.SetIfAbsent(t.Context(), "key", []byte("first"), time.Minute)
	if err != nil || !created {
		t.Fatalf("first SetIfAbsent() = %v, %v, want true", created, err)
	}
	created, err = store.SetIfAbsent(t.Context(), "key", []byte("second"), time.Minute)
	if err != nil || created {
		t.Fatalf("second SetIfAbsent() = %v, %v, want false", created, err)
	}
}

func TestRedisStore_Validation(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client)

	tests := []struct {
		name string
		key  string
		ttl  time.Duration
	}{
		{name: "empty key", ttl: time.Second},
		{name: "blank key", key: "  ", ttl: time.Second},
		{name: "negative ttl", key: "key", ttl: -time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := store.Set(t.Context(), test.key, nil, test.ttl); err == nil {
				t.Fatal("Set() error = nil, want validation error")
			}
		})
	}
}

func TestJSONHelpers(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client)
	type payload struct {
		ID string `json:"id"`
	}

	if err := SetJSON(t.Context(), store, "payload", payload{ID: "42"}, time.Minute); err != nil {
		t.Fatalf("SetJSON() error = %v", err)
	}
	value, err := GetJSON[payload](t.Context(), store, "payload")
	if err != nil || value.ID != "42" {
		t.Fatalf("GetJSON() = %+v, %v", value, err)
	}
}
