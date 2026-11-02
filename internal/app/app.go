package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/application"
	userauthentication "github.com/lihongjie0209/go-api-template/internal/authentication"
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/background"
	"github.com/lihongjie0209/go-api-template/internal/buildinfo"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/datalifecycle"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/dictionary"
	"github.com/lihongjie0209/go-api-template/internal/eventbus"
	"github.com/lihongjie0209/go-api-template/internal/files"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/logging"
	"github.com/lihongjie0209/go-api-template/internal/migration"
	"github.com/lihongjie0209/go-api-template/internal/navigation"
	"github.com/lihongjie0209/go-api-template/internal/objectstorage"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/outbound"
	"github.com/lihongjie0209/go-api-template/internal/permission"
	"github.com/lihongjie0209/go-api-template/internal/platformconfig"
	"github.com/lihongjie0209/go-api-template/internal/policysync"
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	"github.com/lihongjie0209/go-api-template/internal/resourcecatalog"
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

var (
	errDatabaseStartupFailed        = errors.New("database_startup_failed")
	errRedisStartupFailed           = errors.New("redis_startup_failed")
	errRedisShutdownFailed          = errors.New("redis_shutdown_failed")
	errObjectStorageStartupFailed   = errors.New("object_storage_startup_failed")
	errMigrationStartupFailed       = errors.New("migration_startup_failed")
	errResourceCatalogStartupFailed = errors.New("resource_catalog_startup_failed")
	errDataPolicyStartupFailed      = errors.New("data_policy_startup_failed")
	errLoggerStartupFailed          = errors.New("logger_startup_failed")
	errLoggerShutdownFailed         = errors.New("logger_shutdown_failed")
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
		fx.Provide(userauthentication.NewWithSecurity),
		eventbus.Module,
		operationlog.Module,
		securitylog.Module,
		fx.Provide(permission.NewRepository, permission.New),
		fx.Provide(resourceaction.PlatformResourceDefinitions, resourceaction.NewRegistryFromDefinitions),
		fx.Provide(resourcecatalog.New, resourcecatalog.NewService),
		fx.Invoke(startResourceCatalog),
		fx.Provide(newDataPermissionSchemas, datapermission.NewRuntimeEngine, datapermission.NewRepository, newDataPermissionRuntimeLoader, datapermission.NewLifecycleService, datapermission.NewService, datapermission.NewSimulator),
		fx.Invoke(startDataPermissionRuntime),
		fx.Provide(platformconfig.New),
		fx.Provide(application.New),
		fx.Provide(application.NewTenantAccessService),
		fx.Provide(application.NewDictionaryProvider),
		fx.Provide(scheduler.NewHandlerRegistry, scheduler.NewDefinitionService),
		fx.Provide(navigation.New),
		fx.Provide(dictionary.New, dictionary.NewProviderRegistry),
		fx.Invoke(application.RegisterDictionaryProvider),
		fx.Provide(tenant.NewRepository, tenant.New, fx.Annotate(tenant.NewUserResolver, fx.As(new(tenant.UserResolver)), fx.As(new(presentation.ActorResolver))), fx.Annotate(newMemberContactSearcher, fx.As(new(tenant.MemberContactSearcher))), tenant.NewMembershipService, tenant.NewContextService),
		fx.Invoke(func(service *tenant.Service, runtime *datapermission.RuntimeLoader) {
			service.SetPolicyRuntime(runtime)
		}),
		fx.Provide(tenant.NewDepartmentService),
		fx.Provide(idempotency.New),
		fx.Provide(observability.NewMetrics),
		outbound.Module,
		fx.Provide(authorization.New),
		fx.Provide(
			fx.Annotate(tenant.NewMemberCapabilityProvider, fx.ResultTags(`group:"row-capability-providers"`)),
			fx.Annotate(tenant.NewDepartmentCapabilityProvider, fx.ResultTags(`group:"row-capability-providers"`)),
			fx.Annotate(authorization.NewTenantRoleCapabilityProvider, fx.ResultTags(`group:"row-capability-providers"`)),
			fx.Annotate(authorization.NewRowCapabilityRegistry, fx.ParamTags(`group:"row-capability-providers"`)),
			authorization.NewPolicyRevisions,
			authorization.NewCapabilityService,
		),
		fx.Provide(authorization.NewTenantAuthorizationService),
		fx.Provide(authorization.NewPlatformGrantService),
		scheduler.Module,
		grpctransport.Module,
		httptransport.Module,
		fx.StartTimeout(cfg.App.StartupTimeout),
		fx.StopTimeout(cfg.App.ShutdownTimeout),
	)
}

func newDataPermissionSchemas() (*datapermission.SchemaRegistry, error) {
	return datapermission.NewSchemaRegistry(tenant.NewMemberDataPermissionSchema(), tenant.NewDepartmentDataPermissionSchema(), authorization.NewTenantRoleDataPermissionSchema())
}

func startResourceCatalog(lc fx.Lifecycle, cfg config.Config, syncer *resourcecatalog.Syncer) {
	lc.Append(fx.StartHook(func(ctx context.Context) error {
		if !cfg.Database.Enabled {
			return nil
		}
		if err := syncer.Sync(ctx, buildinfo.BuildTime, buildinfo.Commit); err != nil {
			return observability.SafeError(err, errResourceCatalogStartupFailed)
		}
		return nil
	}))
}

func newDataPermissionRuntimeLoader(repository *datapermission.Repository, engine *datapermission.Engine, client *redis.Client, cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) *datapermission.RuntimeLoader {
	loader := datapermission.NewRuntimeLoader(repository, engine)
	loader.ConfigureSync(policysync.NewSnapshot(client, cfg.RedisKeyPrefix()+"policy:data-permission:changed", "data_permission", cfg.PolicySync.PollInterval, cfg.PolicySync.Timeout, repository.Revision, loader.RefreshSnapshot, logger, metrics))
	return loader
}

func startDataPermissionRuntime(lc fx.Lifecycle, cfg config.Config, loader *datapermission.RuntimeLoader) {
	worker := background.New(loader.Run)
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		if !cfg.Database.Enabled {
			return nil
		}
		if err := loader.Initialize(ctx); err != nil {
			return observability.SafeError(err, errDataPolicyStartupFailed)
		}
		worker.Start()
		return nil
	}, OnStop: worker.Stop})
}

func registerFileDeletionWorker(lc fx.Lifecycle, service *files.Service) {
	worker := background.New(service.RunDeletionWorker)
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			if err := service.ValidateDependencies(); err != nil {
				return err
			}
			worker.Start()
			return nil
		},
		OnStop: worker.Stop,
	})
}

func runStartupMigration(cfg config.Config, logger *slog.Logger) error {
	if !cfg.Migration.AutoUp {
		return nil
	}
	started := time.Now()
	logger.Info("running startup database migration", "path", cfg.Migration.Path)
	if err := migration.Run(cfg.Migration, "up", 0); err != nil {
		return observability.SafeError(err, errMigrationStartupFailed)
	}
	logger.Info("startup database migration completed", "duration", time.Since(started))
	return nil
}

func newLogger(lc fx.Lifecycle, cfg config.Config) (*slog.Logger, error) {
	logger, closer, err := logging.New(cfg.Log)
	if err != nil {
		return nil, observability.SafeError(err, errLoggerStartupFailed)
	}
	lc.Append(fx.StopHook(func() error { return observability.SafeError(closer.Close(), errLoggerShutdownFailed) }))
	return logger.With(
		"service", cfg.App.Name,
		"environment", cfg.Runtime.ActiveProfile,
		"version", buildinfo.Version,
		"commit", buildinfo.Commit,
		"build_time", buildinfo.BuildTime,
	), nil
}

func newDatabase(lc fx.Lifecycle, cfg config.Config) (*sqlx.DB, error) {
	if !cfg.Database.Enabled {
		return nil, nil
	}
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		return nil, observability.SafeError(err, errDatabaseStartupFailed)
	}
	lc.Append(fx.StopHook(func() error { return db.Close() }))
	return db, nil
}

func newRedis(lc fx.Lifecycle, cfg config.Config, tracing *observability.Tracing) (*redis.Client, error) {
	return newRedisWithOpener(lc, cfg, tracing, cache.Open)
}

func newRedisWithOpener(lc fx.Lifecycle, cfg config.Config, tracing *observability.Tracing, open func(context.Context, config.Redis) (*redis.Client, error)) (*redis.Client, error) {
	client, err := open(context.Background(), cfg.Redis)
	if err != nil {
		return nil, observability.SafeError(err, errRedisStartupFailed)
	}
	var closeOnce sync.Once
	var closeErr error
	closeClient := func() error {
		closeOnce.Do(func() { closeErr = client.Close() })
		return observability.SafeError(closeErr, errRedisShutdownFailed)
	}
	if tracing.Enabled() {
		lc.Append(fx.Hook{
			OnStart: func(ctx context.Context) error {
				if err := ctx.Err(); err != nil {
					_ = closeClient()
					return err
				}
				if err := redisotel.InstrumentTracing(client); err != nil {
					_ = closeClient()
					return observability.SafeError(err, errRedisStartupFailed)
				}
				return nil
			},
			OnStop: func(context.Context) error { return closeClient() },
		})
	} else {
		lc.Append(fx.StopHook(closeClient))
	}
	return client, nil
}

func redisKeyPrefix(cfg config.Config) string {
	return cfg.RedisKeyPrefix()
}

func newCacheStore(client *redis.Client, cfg config.Config, metrics *observability.Metrics) cache.Store {
	return cache.ObserveStore(cache.NewRedisStore(client, cache.WithKeyPrefix(redisKeyPrefix(cfg))), metrics, "redis")
}

func newLocker(client *redis.Client, cfg config.Config, metrics *observability.Metrics) cache.Locker {
	return cache.ObserveLocker(cache.NewLocker(client, cache.WithLockKeyPrefix(redisKeyPrefix(cfg)+"lock:")), metrics, "redis")
}

func newObjectStorage(cfg config.Config, metrics *observability.Metrics) (objectstorage.Store, error) {
	return newObjectStorageWithFactory(cfg, metrics, objectstorage.New)
}

func newObjectStorageWithFactory(cfg config.Config, metrics *observability.Metrics, factory func(context.Context, config.ObjectStorage) (objectstorage.Store, error)) (objectstorage.Store, error) {
	if !cfg.ObjectStorage.Enabled {
		return nil, nil
	}
	startupCtx, cancel := context.WithTimeout(context.Background(), cfg.ObjectStorage.Timeout)
	defer cancel()
	store, err := factory(startupCtx, cfg.ObjectStorage)
	if err != nil {
		return nil, observability.SafeError(err, errObjectStorageStartupFailed)
	}
	if err := startupCtx.Err(); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errObjectStorageStartupFailed
	}
	store = objectstorage.WithTimeout(store, cfg.ObjectStorage.Timeout)
	return objectstorage.Observe(store, metrics, cfg.ObjectStorage.Provider), nil
}

var DatabaseModule = fx.Module("database", fx.Provide(newDatabase, database.NewTransactor), fx.Invoke(func(db *sqlx.DB, logger *slog.Logger) {
	if db == nil {
		logger.Warn("database is disabled")
	}
}))
var MigrationModule = fx.Module("migration", fx.Invoke(runStartupMigration))
var CacheModule = fx.Module("cache", fx.Provide(newRedis, newCacheStore, newLocker))
var ObjectStorageModule = fx.Module("object-storage", fx.Provide(newObjectStorage))
