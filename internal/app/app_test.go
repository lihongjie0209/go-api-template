package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/files"
	"github.com/lihongjie0209/go-api-template/internal/objectstorage"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"
)

type startupTestLifecycle struct{}

func (*startupTestLifecycle) Append(fx.Hook) {}

func TestInfrastructureStartupErrorsAreSafe(t *testing.T) {
	t.Parallel()
	lifecycle := new(startupTestLifecycle)
	if _, err := newDatabase(lifecycle, config.Config{Database: config.Database{Enabled: true, Type: "unsupported", DSN: "postgres://user:secret-value@host/db"}}); err == nil {
		t.Fatal("invalid database started")
	} else {
		assertStartupReason(t, err, "database_startup_failed")
	}
	if _, err := newRedisWithOpener(lifecycle, config.Config{}, new(observability.Tracing), func(context.Context, config.Redis) (*redis.Client, error) {
		return nil, errors.New("redis://user:secret-value@host:6379 unavailable")
	}); err == nil {
		t.Fatal("unavailable Redis started")
	} else {
		assertStartupReason(t, err, "redis_startup_failed")
	}
	if _, err := newObjectStorage(config.Config{ObjectStorage: config.ObjectStorage{
		Enabled: true, Provider: "unsupported", Bucket: "bucket", Region: "region", PresignTTL: time.Minute, Timeout: time.Second,
	}}, nil); err == nil {
		t.Fatal("unsupported object storage started")
	} else {
		assertStartupReason(t, err, "object_storage_startup_failed")
	}
}

func TestLoggerStartupDoesNotExposeFilesystemPath(t *testing.T) {
	t.Parallel()
	blockingFile := filepath.Join(t.TempDir(), "secret-value")
	if err := os.WriteFile(blockingFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger, err := newLogger(new(startupTestLifecycle), config.Config{Log: config.Log{File: filepath.Join(blockingFile, "service.log"), Level: "info", Format: "json"}})
	if logger != nil {
		t.Fatal("logger unexpectedly started")
	}
	assertStartupReason(t, err, "logger_startup_failed")
}

func TestObjectStorageStartupIsBoundedAndRejectsMissingStore(t *testing.T) {
	t.Parallel()
	cfg := config.Config{ObjectStorage: config.ObjectStorage{Enabled: true, Timeout: 20 * time.Millisecond}}
	store, err := newObjectStorageWithFactory(cfg, nil, func(ctx context.Context, _ config.ObjectStorage) (objectstorage.Store, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("object storage constructor received no deadline")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if store != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("object storage constructor after deadline = store:%v error:%v", store, err)
	}
	store, err = newObjectStorageWithFactory(cfg, nil, func(context.Context, config.ObjectStorage) (objectstorage.Store, error) {
		return nil, nil
	})
	if store != nil || !errors.Is(err, errObjectStorageStartupFailed) {
		t.Fatalf("missing object storage client accepted: store:%v error:%v", store, err)
	}
}

func assertStartupReason(t *testing.T, err error, want string) {
	t.Helper()
	if reason, ok := observability.SafeReason(err); !ok || reason != want || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("startup error %q was not classified as %q", err, want)
	}
}

func TestNew_DependencyGraph(t *testing.T) {
	t.Parallel()
	redisServer := miniredis.RunT(t)
	cfg := config.Config{
		App:  config.App{Name: "test", Env: "test", ShutdownTimeout: time.Second},
		HTTP: config.HTTP{Address: "127.0.0.1:0", ReadTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, MaxBodyBytes: 1024},
		Log:  config.Log{Level: "error", Format: "json", File: filepath.Join(t.TempDir(), "app.log"), MaxSizeMB: 1, MaxBackups: 1, MaxAgeDays: 1},
		JWT:  config.JWT{Issuer: "test", TTL: time.Hour},
		Cron: config.Cron{Timezone: "UTC"},
		Redis: config.Redis{
			Address:      redisServer.Addr(),
			DialTimeout:  time.Second,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
		},
	}
	application := New(cfg)
	if err := application.Err(); err != nil {
		t.Fatalf("New() dependency graph error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := application.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestFileWorkerRejectsMissingRuntimeDependencies(t *testing.T) {
	t.Parallel()
	service := files.New(nil, nil, nil, nil, nil, nil, nil, config.Config{Files: config.Files{Enabled: true}})
	application := fx.New(fx.Supply(service), fx.Invoke(registerFileDeletionWorker), fx.NopLogger)
	if err := application.Err(); err != nil {
		t.Fatalf("build file worker: %v", err)
	}
	if err := application.Start(t.Context()); err == nil {
		t.Fatal("file worker started without required dependencies")
	}
}
