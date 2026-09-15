//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	hellov1 "github.com/lihongjie0209/go-api-template/gen/hello/v1"
	"github.com/lihongjie0209/go-api-template/internal/app"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	appdb "github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/routepolicy"
	"github.com/lihongjie0209/go-api-template/internal/testutil"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	rediscontainer "github.com/testcontainers/testcontainers-go/modules/redis"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
)

func TestHTTPAndGRPCEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	postgresContainer, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase("app"), postgres.WithUsername("app"), postgres.WithPassword("app"), postgres.BasicWaitStrategies(), postgres.WithSQLDriver("pgx"))
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, postgresContainer)
	dsn, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	migrationPath, _ := filepath.Abs(filepath.Join("..", "migrations", "postgres"))

	redisContainer, err := rediscontainer.Run(ctx, "redis:7.4-alpine")
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, redisContainer)
	redisURL, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	redisOptions, err := goredis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}

	httpAddress := freeAddress(t)
	grpcAddress := freeAddress(t)
	const secret = "01234567890123456789012345678901"
	jwtConfig, keyErr := testutil.JWTConfig()
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	jwtConfig.Issuer = "integration"
	cfg := config.Config{
		Runtime:       config.Runtime{ActiveProfile: "integration"},
		App:           config.App{Name: "integration", Env: "integration", ShutdownTimeout: 10 * time.Second},
		HTTP:          config.HTTP{Address: httpAddress, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, RequestTimeout: 5 * time.Second, MaxBodyBytes: 1 << 20},
		GRPC:          config.GRPC{Enabled: true, Address: grpcAddress, MaxReceiveBytes: 4 << 20},
		Log:           config.Log{Level: "error", Format: "json", File: filepath.Join(t.TempDir(), "app.log"), MaxSizeMB: 1, MaxBackups: 1, MaxAgeDays: 1},
		Database:      config.Database{Enabled: true, Type: "postgres", DSN: dsn, MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute, PingTimeout: 10 * time.Second},
		Migration:     config.Migration{AutoUp: true, Path: migrationPath, DatabaseURL: dsn, Table: "integration_e2e_schema_migrations"},
		Redis:         config.Redis{Enabled: true, Address: redisOptions.Addr, DB: redisOptions.DB, DialTimeout: 5 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second},
		Health:        config.Health{DatabaseTimeout: 2 * time.Second, RedisTimeout: 2 * time.Second},
		Observability: config.Observability{MetricsEnabled: true},
		JWT:           jwtConfig,
		Auth:          config.Auth{ClientID: "client", ClientSecret: "secret", PSK: config.PSK{Enabled: true, Key: secret}},
		Authorization: config.Authorization{Enabled: true, PolicyRefreshInterval: 100 * time.Millisecond},
		Cron:          config.Cron{Enabled: false, Timezone: "UTC"},
		User:          config.User{CacheTTL: time.Minute, LockTTL: 10 * time.Second, LockRetryDelay: 20 * time.Millisecond},
		Idempotency:   config.Idempotency{Enabled: true, ProcessingTTL: 30 * time.Second, ResultTTL: time.Hour, FailureTTL: time.Minute},
	}
	application := app.New(cfg)
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = application.Stop(stopCtx)
	})
	token, err := auth.New(cfg).Issue("client")
	if err != nil {
		t.Fatal(err)
	}

	baseURL := "http://" + httpAddress
	if status := postJSON(t, baseURL+"/api/v1/version", "", "", `{}`); status != http.StatusInternalServerError {
		t.Fatalf("missing policy status = %d", status)
	}
	seedRoutePolicies(t, ctx, cfg, dsn)
	redisClient := goredis.NewClient(redisOptions)
	t.Cleanup(func() { _ = redisClient.Close() })
	if err := redisClient.Publish(ctx, "integration:integration:route-policy:changed", "refresh").Err(); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, baseURL+"/api/v1/version", http.StatusOK)
	if status := postJSON(t, baseURL+"/api/v1/version", "", "", `{}`); status != http.StatusOK {
		t.Fatalf("public version status = %d", status)
	}
	if status := postJSON(t, baseURL+"/api/v1/me", "Bearer "+token, "", `{}`); status != http.StatusOK {
		t.Fatalf("JWT status = %d", status)
	}

	connection, err := grpc.NewClient(grpcAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	healthResponse, err := grpc_health_v1.NewHealthClient(connection).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil || healthResponse.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("health = %v, %v", healthResponse, err)
	}
	pskCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "PSK "+secret)
	if _, err := hellov1.NewHelloServiceClient(connection).Ping(pskCtx, &hellov1.PingRequest{Message: "hello"}); err != nil {
		t.Fatalf("PSK Ping: %v", err)
	}
}

func seedRoutePolicies(t *testing.T, ctx context.Context, cfg config.Config, dsn string) {
	t.Helper()
	db, err := appdb.Open(ctx, config.Database{Type: "postgres", DSN: dsn, MaxOpenConns: 2, MaxIdleConns: 1, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute, PingTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	actorCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "integration-policy-bootstrap", Type: platformprincipal.TypeServiceAccount})
	routes := []struct {
		protocol, method, path, expression string
	}{
		{protocol: "http", method: http.MethodPost, path: "/api/v1/version", expression: "anonymous"},
		{protocol: "http", method: http.MethodPost, path: "/api/v1/me", expression: "authenticated"},
		{protocol: "grpc", method: "call", path: hellov1.HelloService_Ping_FullMethodName, expression: `authenticated && principal_type == "service_account"`},
	}
	transactor := appdb.NewTransactor(db)
	if err := transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		for _, item := range routes {
			route, routeErr := routepolicy.NewRoute(item.protocol, item.method, item.path, cfg.App.Name, "")
			if routeErr != nil {
				return routeErr
			}
			now := time.Now()
			query := tx.Rebind(`INSERT INTO route_policy_definitions(id,route_id,expression,description,priority,status,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,0,'active',?,?,?,?,1)`)
			if _, routeErr = tx.ExecContext(actorCtx, query, route.ID, route.ID, item.expression, "integration policy", now, "ignored", now, "ignored"); routeErr != nil {
				return routeErr
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func waitForStatus(t *testing.T, target string, expected int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if status := postJSON(t, target, "", "", `{}`); status == expected {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s did not reach status %d", target, expected)
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func postJSON(t *testing.T, target, authorization, key, body string) int {
	t.Helper()
	_, status := postJSONBody(t, target, authorization, key, body)
	return status
}
func postJSONBody(t *testing.T, target, authorization, key, body string) ([]byte, int) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var validJSON any
	if err := json.Unmarshal(data, &validJSON); err != nil {
		t.Fatalf("invalid JSON response: %v (%s)", err, data)
	}
	return data, response.StatusCode
}

var _ = fmt.Sprintf
