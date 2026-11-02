package health

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/redis/go-redis/v9"
)

type blockingPingConnector struct {
	started chan struct{}
	release chan struct{}
}

func (c *blockingPingConnector) Connect(context.Context) (driver.Conn, error) {
	return &blockingPingConn{started: c.started, release: c.release}, nil
}
func (*blockingPingConnector) Driver() driver.Driver { return blockingPingDriver{} }

type blockingPingDriver struct{}

func (blockingPingDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("connector is required")
}

type blockingPingConn struct {
	started chan struct{}
	release chan struct{}
}

func (c *blockingPingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is unsupported")
}
func (c *blockingPingConn) Close() error { return nil }
func (c *blockingPingConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are unsupported")
}
func (c *blockingPingConn) Ping(context.Context) error {
	close(c.started)
	<-c.release
	return nil
}

func TestService_ReadyReturnsAfterCallerCancellationWhenDriverIgnoresContext(t *testing.T) {
	connector := &blockingPingConnector{started: make(chan struct{}), release: make(chan struct{})}
	database := sql.OpenDB(connector)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(connector.release) }) }
	t.Cleanup(func() {
		release()
		_ = database.Close()
	})
	service := New(sqlx.NewDb(database, "blocked"), nil, config.Config{
		Database: config.Database{Enabled: true},
		Health:   config.Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second},
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		select {
		case <-connector.started:
			cancel()
		case <-t.Context().Done():
		}
	}()
	fallback := time.AfterFunc(time.Second, release)
	defer fallback.Stop()
	started := time.Now()
	status, ready := service.Ready(ctx)
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Ready() waited %s for a driver that ignored cancellation", elapsed)
	}
	release()
	if ready || status.Status != "not_ready" || status.Dependencies["database"].Status != "down" {
		t.Fatalf("Ready() after canceled blocking driver = %#v, %t", status, ready)
	}
}

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
	service := New(sqlx.NewDb(database, "sqlmock"), client, config.Config{Database: config.Database{Enabled: true}, Health: config.Health{DatabaseTimeout: 10 * time.Millisecond, RedisTimeout: time.Second}})

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

func TestService_ReadyFailsWhenMandatoryRedisClientIsMissing(t *testing.T) {
	t.Parallel()
	service := New(nil, nil, config.Config{Health: config.Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second}})
	status, ready := service.Ready(t.Context())
	if ready || status.Dependencies["redis"].Status != "down" {
		t.Fatalf("Ready() = %#v, %t", status, ready)
	}
}

func TestService_ReadyFailsWhenEnabledDatabaseHandleIsMissing(t *testing.T) {
	t.Parallel()
	server, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	service := New(nil, client, config.Config{Database: config.Database{Enabled: true}, Health: config.Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second}})
	status, ready := service.Ready(t.Context())
	if ready || status.Status != "not_ready" || status.Dependencies["database"].Status != "down" || status.Dependencies["redis"].Status != "up" {
		t.Fatalf("Ready() = %#v, %t", status, ready)
	}
}

func TestService_ReadyReportsExplicitlyDisabledDatabaseEvenWithHandle(t *testing.T) {
	t.Parallel()
	database, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	service := New(sqlx.NewDb(database, "sqlmock"), client, config.Config{
		Database: config.Database{Enabled: false},
		Health:   config.Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second},
	})
	status, ready := service.Ready(t.Context())
	if !ready || status.Dependencies["database"].Status != "disabled" || status.Dependencies["redis"].Status != "up" {
		t.Fatalf("Ready() = %#v, %t", status, ready)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_ReadyHonorsParentCancellation(t *testing.T) {
	t.Parallel()
	database, _, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	client := redis.NewClient(&redis.Options{Addr: "192.0.2.1:6379", DialTimeout: time.Second})
	t.Cleanup(func() { _ = client.Close() })
	service := New(sqlx.NewDb(database, "sqlmock"), client, config.Config{Database: config.Database{Enabled: true}, Health: config.Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second}})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	started := time.Now()
	status, ready := service.Ready(ctx)
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("Ready() took %s after parent cancellation", elapsed)
	}
	if ready || status.Dependencies["database"].Status != "down" || status.Dependencies["redis"].Status != "down" {
		t.Fatalf("Ready() = %#v, %t", status, ready)
	}
}
