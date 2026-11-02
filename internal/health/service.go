package health

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
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
	db              *sqlx.DB
	redis           *redis.Client
	databaseEnabled bool
	cfg             config.Health
	probes          singleflight.Group
}
type result struct {
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
	if ctx.Err() != nil {
		return Status{Status: "not_ready", Dependencies: map[string]Dependency{
			"database": {Status: "down"}, "redis": {Status: "down"},
		}}, false
	}
	// A driver may ignore its Context, so the aggregation also enforces each
	// dependency's deadline independently of that driver's return.
	databaseTimer := time.NewTimer(s.cfg.DatabaseTimeout)
	defer databaseTimer.Stop()
	redisTimer := time.NewTimer(s.cfg.RedisTimeout)
	defer redisTimer.Stop()
	databaseDeadline := databaseTimer.C
	redisDeadline := redisTimer.C
	// DoChan publishes to a buffered channel for each caller. A timed-out
	// caller cannot block the shared probe when it eventually completes.
	// A shared probe must outlive the first caller's cancellation so another
	// waiting readiness request cannot inherit its failure. Each probe still
	// has its own configured deadline, while every caller has separate timers.
	databaseResults := s.probes.DoChan("database", func() (any, error) { return s.checkDatabase(), nil })
	redisResults := s.probes.DoChan("redis", func() (any, error) { return s.checkRedis(), nil })
	status := Status{Status: "ready", Dependencies: make(map[string]Dependency, 2)}
	ready := true
	for len(status.Dependencies) < 2 {
		select {
		case outcome := <-databaseResults:
			item := outcome.Val.(result)
			status.Dependencies["database"] = item.dependency
			ready = ready && item.ready
			databaseResults = nil
			databaseDeadline = nil
		case outcome := <-redisResults:
			item := outcome.Val.(result)
			status.Dependencies["redis"] = item.dependency
			ready = ready && item.ready
			redisResults = nil
			redisDeadline = nil
		case <-databaseDeadline:
			databaseDeadline = nil
			databaseResults = nil
			status.Dependencies["database"] = Dependency{Status: "down"}
			ready = false
		case <-redisDeadline:
			redisDeadline = nil
			redisResults = nil
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

func (s *Service) checkDatabase() result {
	if !s.databaseEnabled {
		return result{dependency: Dependency{Status: "disabled"}, ready: true}
	}
	if s.db == nil {
		return result{dependency: Dependency{Status: "down"}, ready: false}
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.DatabaseTimeout)
	defer cancel()
	started := time.Now()
	err := s.db.PingContext(ctx)
	if deadlineErr := ctx.Err(); deadlineErr != nil {
		err = deadlineErr
	}
	return checkResult(started, err)
}

func (s *Service) checkRedis() result {
	if s.redis == nil {
		return result{dependency: Dependency{Status: "down"}, ready: false}
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.RedisTimeout)
	defer cancel()
	started := time.Now()
	err := s.redis.Ping(ctx).Err()
	if deadlineErr := ctx.Err(); deadlineErr != nil {
		err = deadlineErr
	}
	return checkResult(started, err)
}

func checkResult(started time.Time, err error) result {
	dependency := Dependency{Status: "up", Latency: time.Since(started).String()}
	if err != nil {
		dependency.Status = "down"
		return result{dependency: dependency, ready: false}
	}
	return result{dependency: dependency, ready: true}
}
