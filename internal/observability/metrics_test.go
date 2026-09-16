package observability

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/redis/go-redis/v9"
)

func TestMetrics_Collect(t *testing.T) {
	t.Parallel()
	metrics := NewMetrics(config.Config{Observability: config.Observability{MetricsEnabled: true}}, nil, nil)
	metrics.HTTPRequests.WithLabelValues("POST", "/test", "200").Inc()
	metrics.HTTPDuration.WithLabelValues("POST", "/test").Observe(0.01)
	metrics.ObserveCron("sample", "success", time.Now())
	metrics.ObserveInfrastructure("cache", "redis", "get", "hit", time.Now())
	families, err := metrics.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"http_requests_total": false, "http_request_duration_seconds": false, "cron_runs_total": false, "infrastructure_operations_total": false, "service_build_info": false}
	for _, family := range families {
		if _, ok := want[family.GetName()]; ok {
			want[family.GetName()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("metric %q not gathered", name)
		}
	}
}

func TestMetrics_CollectDatabaseAndRedisPoolStats(t *testing.T) {
	t.Parallel()
	rawDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	if err := redisClient.Ping(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	metrics := NewMetrics(config.Config{Observability: config.Observability{MetricsEnabled: true}}, sqlx.NewDb(rawDB, "sqlmock"), redisClient)
	families, err := metrics.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"go_sql_open_connections": false, "redis_pool_connections": false, "redis_pool_hits_total": false}
	for _, family := range families {
		if _, ok := want[family.GetName()]; ok {
			want[family.GetName()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("metric %q not gathered", name)
		}
	}
}
