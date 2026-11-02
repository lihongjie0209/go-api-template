package health

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/redis/go-redis/v9"
)

type Status struct {
	Status       string                `json:"status"`
	Dependencies map[string]Dependency `json:"dependencies,omitempty"`
}
type Dependency struct {
	Status  string `json:"status"`
	Latency string `json:"latency,omitempty"`
}
type Service struct {
	db               *sqlx.DB
	redis            *redis.Client
	databaseEnabled  bool
	cfg              config.Health
	databaseInFlight atomic.Bool
	redisInFlight    atomic.Bool
}
type result struct {
	name       string
	dependency Dependency
	ready      bool
}

func New(db *sqlx.DB, client *redis.Client, cfg config.Config) *Service {
	healthConfig := cfg.Health
	if healthConfig.DatabaseTimeout <= 0 {
		healthConfig.DatabaseTimeout = 2 * time.Second
	}
	if healthConfig.RedisTimeout <= 0 {
		healthConfig.RedisTimeout = 2 * time.Second
	}
	return &Service{db: db, redis: client, databaseEnabled: cfg.Database.Enabled, cfg: healthConfig}
}
func (s *Service) Live() Status { return Status{Status: "up"} }

func (s *Service) Ready(ctx context.Context) (Status, bool) {
	// A driver may ignore its Context, so the aggregation also enforces each
	// dependency's deadline independently of that driver's return.
	databaseTimer := time.NewTimer(s.cfg.DatabaseTimeout)
	defer databaseTimer.Stop()
	redisTimer := time.NewTimer(s.cfg.RedisTimeout)
	defer redisTimer.Stop()
	databaseDeadline := databaseTimer.C
	redisDeadline := redisTimer.C
	// Keep one slot per dependency so a cancelled caller can never leave a
	// completed probe blocked while publishing its result.
	results := make(chan result, 2)
	go s.checkDatabase(ctx, results)
	go s.checkRedis(ctx, results)
	status := Status{Status: "ready", Dependencies: make(map[string]Dependency, 2)}
	ready := true
	for len(status.Dependencies) < 2 {
		select {
		case item := <-results:
			if _, found := status.Dependencies[item.name]; found {
				continue
			}
			status.Dependencies[item.name] = item.dependency
			ready = ready && item.ready
			if item.name == "database" {
				databaseDeadline = nil
			} else {
				redisDeadline = nil
			}
		case <-databaseDeadline:
			databaseDeadline = nil
			status.Dependencies["database"] = Dependency{Status: "down"}
			ready = false
		case <-redisDeadline:
			redisDeadline = nil
			status.Dependencies["redis"] = Dependency{Status: "down"}
			ready = false
		case <-ctx.Done():
			for _, name := range []string{"database", "redis"} {
				if _, found := status.Dependencies[name]; !found {
					status.Dependencies[name] = Dependency{Status: "down"}
				}
			}
			status.Status = "not_ready"
			return status, false
		}
	}
	if ctx.Err() != nil {
		status.Status = "not_ready"
		return status, false
	}
	if !ready {
		status.Status = "not_ready"
	}
	return status, ready
}

func (s *Service) checkDatabase(parent context.Context, results chan<- result) {
	if !s.databaseEnabled {
		results <- result{name: "database", dependency: Dependency{Status: "disabled"}, ready: true}
		return
	}
	if s.db == nil {
		results <- result{name: "database", dependency: Dependency{Status: "down"}, ready: false}
		return
	}
	if !s.databaseInFlight.CompareAndSwap(false, true) {
		results <- result{name: "database", dependency: Dependency{Status: "down"}, ready: false}
		return
	}
	defer s.databaseInFlight.Store(false)
	ctx, cancel := context.WithTimeout(parent, s.cfg.DatabaseTimeout)
	defer cancel()
	started := time.Now()
	results <- checkResult("database", started, s.db.PingContext(ctx))
}

func (s *Service) checkRedis(parent context.Context, results chan<- result) {
	if s.redis == nil {
		results <- result{name: "redis", dependency: Dependency{Status: "down"}, ready: false}
		return
	}
	if !s.redisInFlight.CompareAndSwap(false, true) {
		results <- result{name: "redis", dependency: Dependency{Status: "down"}, ready: false}
		return
	}
	defer s.redisInFlight.Store(false)
	ctx, cancel := context.WithTimeout(parent, s.cfg.RedisTimeout)
	defer cancel()
	started := time.Now()
	results <- checkResult("redis", started, s.redis.Ping(ctx).Err())
}

func checkResult(name string, started time.Time, err error) result {
	dependency := Dependency{Status: "up", Latency: time.Since(started).String()}
	if err != nil {
		dependency.Status = "down"
		return result{name: name, dependency: dependency, ready: false}
	}
	return result{name: name, dependency: dependency, ready: true}
}
