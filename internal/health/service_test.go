package health

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/redis/go-redis/v9"
)

func TestService_Ready(t *testing.T) {
	t.Parallel()
	server, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer func() { _ = client.Close() }()
	service := New(nil, client, config.Config{Health: config.Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second}})
	status, ready := service.Ready(t.Context())
	if !ready {
		t.Fatalf("Ready() ready = false, status = %#v", status)
	}
	if status.Dependencies["database"].Status != "disabled" || status.Dependencies["redis"].Status != "up" {
		t.Fatalf("Ready() dependencies = %#v", status.Dependencies)
	}
}

func TestService_ReadyUsesIndependentDependencyTimeouts(t *testing.T) {
	t.Parallel()
	database, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	mock.ExpectPing().WillDelayFor(200 * time.Millisecond).WillReturnError(context.DeadlineExceeded)
	server, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	service := New(sqlx.NewDb(database, "sqlmock"), client, config.Config{Health: config.Health{DatabaseTimeout: 10 * time.Millisecond, RedisTimeout: time.Second}})

	started := time.Now()
	status, ready := service.Ready(t.Context())
	if elapsed := time.Since(started); elapsed >= 150*time.Millisecond {
		t.Fatalf("Ready() elapsed = %s, database timeout was not applied", elapsed)
	}
	if ready || status.Dependencies["database"].Status != "down" || status.Dependencies["redis"].Status != "up" {
		t.Fatalf("Ready() = %#v, %t", status, ready)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_LiveDoesNotCheckDependencies(t *testing.T) {
	t.Parallel()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	service := New(nil, client, config.Config{})
	if status := service.Live(); status.Status != "up" || status.Dependencies != nil {
		t.Fatalf("Live() = %#v", status)
	}
}

func TestService_ReadyWhenRedisIsDown(t *testing.T) {
	t.Parallel()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: time.Millisecond})
	defer func() { _ = client.Close() }()
	service := New(nil, client, config.Config{Health: config.Health{DatabaseTimeout: time.Second, RedisTimeout: 10 * time.Millisecond}})
	status, ready := service.Ready(t.Context())
	if ready {
		t.Fatal("Ready() ready = true, want false")
	}
	if status.Status != "not_ready" || status.Dependencies["redis"].Status != "down" {
		t.Fatalf("Ready() status = %#v", status)
	}
}
