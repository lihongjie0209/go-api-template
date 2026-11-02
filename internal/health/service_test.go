package health

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/redis/go-redis/v9"
)

type blockingPingConnector struct {
	started      chan struct{}
	release      chan struct{}
	startOnce    sync.Once
	pingCount    atomic.Int32
	honorContext bool
}

type lateSuccessConnector struct{}

func (lateSuccessConnector) Connect(context.Context) (driver.Conn, error) {
	return lateSuccessConn{}, nil
}
func (lateSuccessConnector) Driver() driver.Driver { return blockingPingDriver{} }

type lateSuccessConn struct{}

func (lateSuccessConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is unsupported")
}
func (lateSuccessConn) Close() error { return nil }
func (lateSuccessConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are unsupported")
}
func (lateSuccessConn) Ping(ctx context.Context) error {
	<-ctx.Done()
	return nil // Simulate a driver that reports success after its deadline.
}

func TestService_DatabaseProbeRejectsLateDriverSuccess(t *testing.T) {
	database := sql.OpenDB(lateSuccessConnector{})
	t.Cleanup(func() { _ = database.Close() })
	service := New(sqlx.NewDb(database, "late"), nil, config.Config{
		Database: config.Database{Enabled: true},
		Health:   config.Health{DatabaseTimeout: 10 * time.Millisecond},
	})
	result := service.checkDatabase()
	if result.ready || result.dependency.Status != "down" {
		t.Fatalf("late driver success = %#v, want down", result)
	}
}

func (c *blockingPingConnector) Connect(context.Context) (driver.Conn, error) {
	return &blockingPingConn{connector: c}, nil
}
func (*blockingPingConnector) Driver() driver.Driver { return blockingPingDriver{} }

type blockingPingDriver struct{}

func (blockingPingDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("connector is required")
}

type blockingPingConn struct {
	connector *blockingPingConnector
}

func (c *blockingPingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is unsupported")
}
func (c *blockingPingConn) Close() error { return nil }
func (c *blockingPingConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are unsupported")
}
func (c *blockingPingConn) Ping(ctx context.Context) error {
	c.connector.pingCount.Add(1)
	c.connector.startOnce.Do(func() { close(c.connector.started) })
	if c.connector.honorContext {
		select {
		case <-c.connector.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	<-c.connector.release
	return nil
}

func TestService_ReadySharedProbeSurvivesFirstCallerCancellation(t *testing.T) {
	connector := &blockingPingConnector{started: make(chan struct{}), release: make(chan struct{}), honorContext: true}
	database := sql.OpenDB(connector)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(connector.release) }) }
	t.Cleanup(func() {
		release()
		_ = database.Close()
	})
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	service := New(sqlx.NewDb(database, "blocked"), client, config.Config{
		Database: config.Database{Enabled: true},
		Health:   config.Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second},
	})
	firstCtx, cancelFirst := context.WithCancel(t.Context())
	defer cancelFirst()
	firstResult := make(chan bool, 1)
	go func() {
		_, ready := service.Ready(firstCtx)
		firstResult <- ready
	}()
	select {
	case <-connector.started:
	case <-time.After(time.Second):
		t.Fatal("first readiness probe did not reach the database")
	}
	type readiness struct {
		status Status
		ready  bool
	}
	secondResult := make(chan readiness, 1)
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		status, ready := service.Ready(t.Context())
		secondResult <- readiness{status: status, ready: ready}
	}()
	<-secondStarted
	cancelTimer := time.AfterFunc(100*time.Millisecond, cancelFirst)
	defer cancelTimer.Stop()
	select {
	case ready := <-firstResult:
		if ready {
			t.Fatal("canceled first readiness request reported ready")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first readiness request did not stop after cancellation")
	}
	release()
	var status Status
	var ready bool
	select {
	case result := <-secondResult:
		status, ready = result.status, result.ready
	case <-time.After(2 * time.Second):
		t.Fatal("second readiness request did not finish")
	}
	if !ready || status.Dependencies["database"].Status != "up" {
		t.Fatalf("second readiness result after first caller cancellation = %#v, %t", status, ready)
	}
	if count := connector.pingCount.Load(); count != 1 {
		t.Fatalf("shared database Ping() calls = %d, want 1", count)
	}
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
		Health:   config.Health{DatabaseTimeout: 30 * time.Millisecond, RedisTimeout: time.Second},
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
	if ready || status.Status != "not_ready" || status.Dependencies["database"].Status != "down" {
		t.Fatalf("Ready() after canceled blocking driver = %#v, %t", status, ready)
	}
	for range 10 {
		started = time.Now()
		status, ready = service.Ready(t.Context())
		if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
			t.Fatalf("Ready() waited %s for an in-flight driver", elapsed)
		}
		if ready || status.Dependencies["database"].Status != "down" {
			t.Fatalf("Ready() with an in-flight driver = %#v, %t", status, ready)
		}
	}
	if count := connector.pingCount.Load(); count != 1 {
		t.Fatalf("driver Ping() calls = %d, want 1 while first check is blocked", count)
	}
	release()
}

func TestService_ConcurrentReadyCallsShareHealthyDependencyCheck(t *testing.T) {
	connector := &blockingPingConnector{started: make(chan struct{}), release: make(chan struct{})}
	database := sql.OpenDB(connector)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(connector.release) }) }
	t.Cleanup(func() {
		release()
		_ = database.Close()
	})
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	service := New(sqlx.NewDb(database, "blocked"), client, config.Config{
		Database: config.Database{Enabled: true},
		Health:   config.Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second},
	})
	type readiness struct {
		status Status
		ready  bool
	}
	first := make(chan readiness, 1)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	go func() {
		status, ready := service.Ready(ctx)
		first <- readiness{status: status, ready: ready}
	}()
	select {
	case <-connector.started:
	case <-ctx.Done():
		t.Fatal("first database check never started")
	}
	releaseAfterJoin := time.AfterFunc(100*time.Millisecond, release)
	defer releaseAfterJoin.Stop()
	secondStatus, secondReady := service.Ready(ctx)
	firstResult := <-first
	if !firstResult.ready || !secondReady || firstResult.status.Dependencies["database"].Status != "up" || secondStatus.Dependencies["database"].Status != "up" {
		t.Fatalf("concurrent Ready() calls = (%#v, %t), (%#v, %t)", firstResult.status, firstResult.ready, secondStatus, secondReady)
	}
	if count := connector.pingCount.Load(); count != 1 {
		t.Fatalf("driver Ping() calls = %d, want one shared check", count)
	}
}

func TestService_ReadyHonorsDependencyTimeoutWhenDriverIgnoresContext(t *testing.T) {
	connector := &blockingPingConnector{started: make(chan struct{}), release: make(chan struct{})}
	database := sql.OpenDB(connector)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(connector.release) }) }
	t.Cleanup(func() {
		release()
		_ = database.Close()
	})
	service := New(sqlx.NewDb(database, "blocked"), client, config.Config{
		Database: config.Database{Enabled: true},
		Health:   config.Health{DatabaseTimeout: 100 * time.Millisecond, RedisTimeout: 1500 * time.Millisecond},
	})
	fallback := time.AfterFunc(2*time.Second, release)
	defer fallback.Stop()

	started := time.Now()
	status, ready := service.Ready(t.Context())
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Ready() waited %s for a driver that ignored its dependency timeout", elapsed)
	}
	if ready || status.Status != "not_ready" || status.Dependencies["database"].Status != "down" || status.Dependencies["redis"].Status != "up" {
		t.Fatalf("Ready() with timed-out driver = %#v, %t", status, ready)
	}
	select {
	case <-connector.started:
	default:
		t.Fatal("database driver never started")
	}
	if count := connector.pingCount.Load(); count != 1 {
		t.Fatalf("driver Ping() calls = %d, want 1", count)
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
