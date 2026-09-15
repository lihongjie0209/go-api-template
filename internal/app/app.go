package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jmoiron/sqlx"
	userauthentication "github.com/lihongjie0209/go-api-template/internal/authentication"
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/datalifecycle"
	"github.com/lihongjie0209/go-api-template/internal/eventbus"
	"github.com/lihongjie0209/go-api-template/internal/files"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/logging"
	"github.com/lihongjie0209/go-api-template/internal/menu"
	"github.com/lihongjie0209/go-api-template/internal/migration"
	"github.com/lihongjie0209/go-api-template/internal/objectstorage"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/outbound"
	"github.com/lihongjie0209/go-api-template/internal/permission"
	"github.com/lihongjie0209/go-api-template/internal/platformconfig"
	"github.com/lihongjie0209/go-api-template/internal/routepolicy"
	"github.com/lihongjie0209/go-api-template/internal/scheduler"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	"github.com/lihongjie0209/go-api-template/internal/tenant"
	grpctransport "github.com/lihongjie0209/go-api-template/internal/transport/grpc"
	httptransport "github.com/lihongjie0209/go-api-template/internal/transport/http"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

func New(cfg config.Config) *fx.App {
	return fx.New(
		fx.Supply(cfg),
		fx.Provide(newLogger),
		fx.Provide(observability.NewTracing),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger { return &fxevent.SlogLogger{Logger: logger} }),
		MigrationModule,
		DatabaseModule,
		datalifecycle.Module,
		CacheModule,
		ObjectStorageModule,
		fx.Provide(files.New),
		fx.Invoke(registerFileDeletionWorker),
		fx.Provide(identity.NewRepository, identity.New),
		fx.Provide(userauthentication.New),
		eventbus.Module,
		operationlog.Module,
		securitylog.Module,
		fx.Provide(permission.NewRepository, permission.New),
		fx.Provide(routepolicy.NewRepository, routepolicy.NewCompiler, routepolicy.NewManager, routepolicy.NewService),
		fx.Provide(platformconfig.New),
		fx.Provide(menu.New),
		fx.Provide(tenant.NewRepository, tenant.New, tenant.NewUserResolver, tenant.NewMembershipService, tenant.NewContextService),
		fx.Provide(tenant.NewDepartmentService),
		fx.Provide(idempotency.New),
		fx.Provide(observability.NewMetrics),
		outbound.Module,
		fx.Provide(authorization.New),
		fx.Provide(authorization.NewTenantAuthorizationService),
		scheduler.Module,
		grpctransport.Module,
		httptransport.Module,
		fx.StartTimeout(cfg.App.ShutdownTimeout),
		fx.StopTimeout(cfg.App.ShutdownTimeout),
	)
}

func registerFileDeletionWorker(lc fx.Lifecycle, service *files.Service) {
	workerCtx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go service.RunDeletionWorker(workerCtx)
			return nil
		},
		OnStop: func(context.Context) error {
			cancel()
			return nil
		},
	})
}

func runStartupMigration(cfg config.Config, logger *slog.Logger) error {
	if !cfg.Migration.AutoUp {
		return nil
	}
	started := time.Now()
	logger.Info("running startup database migration", "path", cfg.Migration.Path)
	if err := migration.Run(cfg.Migration, "up", 0); err != nil {
		return fmt.Errorf("startup database migration: %w", err)
	}
	logger.Info("startup database migration completed", "duration", time.Since(started))
	return nil
}

func newLogger(lc fx.Lifecycle, cfg config.Config) (*slog.Logger, error) {
	logger, closer, err := logging.New(cfg.Log)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.StopHook(func() error { return closer.Close() }))
	return logger.With("service", cfg.App.Name, "environment", cfg.Runtime.ActiveProfile), nil
}

func newDatabase(lc fx.Lifecycle, cfg config.Config) (*sqlx.DB, error) {
	if !cfg.Database.Enabled {
		return nil, nil
	}
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.StopHook(func() error { return db.Close() }))
	return db, nil
}

func newRedis(lc fx.Lifecycle, cfg config.Config, tracing *observability.Tracing) (*redis.Client, error) {
	if !cfg.Redis.Enabled {
		return nil, nil
	}
	client, err := cache.Open(context.Background(), cfg.Redis)
	if err != nil {
		return nil, err
	}
	if tracing.Enabled() {
		if err := redisotel.InstrumentTracing(client); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("instrument redis tracing: %w", err)
		}
	}
	lc.Append(fx.StopHook(func() error { return client.Close() }))
	return client, nil
}

func redisKeyPrefix(cfg config.Config) string {
	if cfg.Redis.KeyPrefix != "" {
		return cfg.Redis.KeyPrefix
	}
	return cfg.App.Name + ":"
}

func newCacheStore(client *redis.Client, cfg config.Config, metrics *observability.Metrics) cache.Store {
	if client == nil {
		return nil
	}
	return cache.ObserveStore(cache.NewRedisStore(client, cache.WithKeyPrefix(redisKeyPrefix(cfg))), metrics, "redis")
}

func newLocker(client *redis.Client, cfg config.Config, metrics *observability.Metrics) cache.Locker {
	if client == nil {
		return nil
	}
	return cache.ObserveLocker(cache.NewLocker(client, cache.WithLockKeyPrefix(redisKeyPrefix(cfg)+"lock:")), metrics, "redis")
}

func newObjectStorage(cfg config.Config, metrics *observability.Metrics) (objectstorage.Store, error) {
	store, err := objectstorage.New(context.Background(), cfg.ObjectStorage)
	if err != nil {
		return nil, err
	}
	return objectstorage.Observe(store, metrics, cfg.ObjectStorage.Provider), nil
}

var DatabaseModule = fx.Module("database", fx.Provide(newDatabase, database.NewTransactor), fx.Invoke(func(db *sqlx.DB, logger *slog.Logger) {
	if db == nil {
		logger.Warn("database is disabled")
	}
}))
var MigrationModule = fx.Module("migration", fx.Invoke(runStartupMigration))
var CacheModule = fx.Module("cache", fx.Provide(newRedis, newCacheStore, newLocker), fx.Invoke(func(client *redis.Client, logger *slog.Logger) {
	if client == nil {
		logger.Warn("redis is disabled")
	}
}))
var ObjectStorageModule = fx.Module("object-storage", fx.Provide(newObjectStorage))
