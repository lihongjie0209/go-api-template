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
	"github.com/lihongjie0209/go-api-template/internal/database"
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
		Runtime:         config.Runtime{ActiveProfile: "integration"},
		App:             config.App{Name: "integration", Env: "integration", ShutdownTimeout: 10 * time.Second},
		HTTP:            config.HTTP{Address: httpAddress, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, RequestTimeout: 5 * time.Second, MaxBodyBytes: 1 << 20},
		GRPC:            config.GRPC{Enabled: true, Address: grpcAddress, MaxReceiveBytes: 4 << 20},
		Log:             config.Log{Level: "error", Format: "json", File: filepath.Join(t.TempDir(), "app.log"), MaxSizeMB: 1, MaxBackups: 1, MaxAgeDays: 1},
		Database:        config.Database{Enabled: true, Type: "postgres", DSN: dsn, MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute, PingTimeout: 10 * time.Second},
		Migration:       config.Migration{AutoUp: true, Path: migrationPath, DatabaseURL: dsn, Table: "integration_e2e_schema_migrations"},
		Redis:           config.Redis{Address: redisOptions.Addr, DB: redisOptions.DB, DialTimeout: 5 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second},
		Health:          config.Health{DatabaseTimeout: 2 * time.Second, RedisTimeout: 2 * time.Second},
		Observability:   config.Observability{MetricsEnabled: true},
		JWT:             jwtConfig,
		Auth:            config.Auth{PSK: config.PSK{Enabled: true, Key: secret}},
		Cron:            config.Cron{Enabled: false, Timezone: "UTC"},
		User:            config.User{CacheTTL: time.Minute},
		DistributedLock: config.DistributedLock{TTL: 10 * time.Second, RetryDelay: 20 * time.Millisecond},
		Idempotency:     config.Idempotency{Enabled: true, ProcessingTTL: 30 * time.Second, ResultTTL: time.Hour, FailureTTL: time.Minute},
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
	seedServiceAccount(t, ctx, cfg.Database, "client")
	token, err := auth.New(cfg).Issue("client")
	if err != nil {
		t.Fatal(err)
	}

	baseURL := "http://" + httpAddress
	if status := postJSON(t, baseURL+"/live", "", "", `{}`); status != http.StatusOK {
		t.Fatalf("liveness status = %d", status)
	}
	if status := postJSON(t, baseURL+"/ready", "", "", `{}`); status != http.StatusOK {
		t.Fatalf("readiness status = %d", status)
	}
	if status := postJSON(t, baseURL+"/api/v1/version", "", "", `{}`); status != http.StatusOK {
		t.Fatalf("public version status = %d", status)
	}
	if status := postJSON(t, baseURL+"/api/v1/example/ping", "PSK "+secret, "", `{"message":"hello"}`); status != http.StatusOK {
		t.Fatalf("PSK status = %d", status)
	}

	connection, err := grpc.NewClient(grpcAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	healthClient := grpc_health_v1.NewHealthClient(connection)
	healthResponse, err := healthClient.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil || healthResponse.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("health = %v, %v", healthResponse, err)
	}
	watch, err := healthClient.Watch(ctx, &grpc_health_v1.HealthCheckRequest{Service: cfg.App.Name})
	if err != nil {
		t.Fatalf("health watch: %v", err)
	}
	healthUpdate, err := watch.Recv()
	if err != nil || healthUpdate.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("health watch = %v, %v", healthUpdate, err)
	}
	pskCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "PSK "+secret)
	if _, err := hellov1.NewHelloServiceClient(connection).Ping(pskCtx, &hellov1.PingRequest{Message: "hello"}); err != nil {
		t.Fatalf("PSK Ping: %v", err)
	}
	jwtCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	if _, err := hellov1.NewHelloServiceClient(connection).Ping(jwtCtx, &hellov1.PingRequest{Message: "hello"}); err != nil {
		t.Fatalf("JWT Ping: %v", err)
	}
}

func seedServiceAccount(t *testing.T, ctx context.Context, cfg config.Database, id string) {
	t.Helper()
	db, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	transactor := database.NewTransactor(db)
	auditCtx := platformprincipal.SystemContext(ctx, "integration-test")
	err = transactor.Within(auditCtx, nil, func(tx *sqlx.Tx) error {
		query := tx.Rebind(`INSERT INTO identity_service_accounts
			(id,client_id,name,description,secret_hash,status,created_at,created_by,updated_at,updated_by,version)
			VALUES (?,?,?,?,?,'active',?,?,?,?,1)`)
		now := time.Now()
		_, execErr := tx.ExecContext(auditCtx, query, id, id, "Integration client", "gRPC JWT E2E fixture", "unused", now, "integration-test", now, "integration-test")
		return execErr
	})
	if err != nil {
		t.Fatalf("seed service account: %v", err)
	}
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
