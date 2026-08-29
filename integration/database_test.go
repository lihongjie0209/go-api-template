//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	appdb "github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/migration"
	"github.com/lihongjie0209/go-api-template/internal/user"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestRepositoryAndMigrations(t *testing.T) {
	for _, databaseType := range []string{"postgres", "mysql"} {
		t.Run(databaseType, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			dsn, migrationURL := startDatabase(t, ctx, databaseType)
			migrationPath, err := filepath.Abs(filepath.Join("..", "migrations", databaseType))
			if err != nil {
				t.Fatal(err)
			}
			schema := ""
			if databaseType == "postgres" {
				schema = "integration_postgres"
			}
			migrationCfg := config.Migration{Path: migrationPath, DatabaseURL: migrationURL, Table: "integration_" + databaseType + "_schema_migrations", Schema: schema, CreateSchema: schema != ""}
			migrationErrors := make(chan error, 3)
			var migrations sync.WaitGroup
			for range 3 {
				migrations.Add(1)
				go func() {
					defer migrations.Done()
					migrationErrors <- migration.Run(migrationCfg, "up", 0)
				}()
			}
			migrations.Wait()
			close(migrationErrors)
			for err := range migrationErrors {
				if err != nil {
					t.Fatalf("concurrent migration up: %v", err)
				}
			}

			db, err := appdb.Open(ctx, config.Database{Type: databaseType, DSN: dsn, Schema: schema, MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute, PingTimeout: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			repository := user.NewRepository(db)
			transactor := appdb.NewTransactor(db)
			now := time.Now().UTC().Truncate(time.Microsecond)
			created := user.User{ID: uuid.NewString(), Name: "Alice", Email: "alice-" + databaseType + "@example.com", Version: 1, CreatedAt: now, UpdatedAt: now}
			if err := transactor.Within(ctx, nil, func(tx *sqlx.Tx) error { return repository.Create(ctx, tx, created) }); err != nil {
				t.Fatalf("create: %v", err)
			}
			found, err := repository.Get(ctx, created.ID)
			if err != nil || found.Email != created.Email {
				t.Fatalf("get = %+v, %v", found, err)
			}
			items, total, err := repository.List(ctx, 10, 0)
			if err != nil || total != 1 || len(items) != 1 {
				t.Fatalf("list total=%d len=%d err=%v", total, len(items), err)
			}
			found.Name, found.Version, found.UpdatedAt = "Alice Updated", 1, now.Add(time.Second)
			if err := repository.Update(ctx, found); err != nil {
				t.Fatalf("update: %v", err)
			}
			updated, err := repository.Get(ctx, created.ID)
			if err != nil || updated.Version != 2 {
				t.Fatalf("updated = %+v, %v", updated, err)
			}
			if err := repository.Delete(ctx, updated.ID, updated.Version); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if _, err := repository.Get(ctx, updated.ID); !errors.Is(err, user.ErrNotFound) {
				t.Fatalf("get deleted error = %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if err := migration.Run(migrationCfg, "down", 0); err != nil {
				t.Fatalf("migration down: %v", err)
			}
		})
	}
}

func startDatabase(t *testing.T, ctx context.Context, databaseType string) (string, string) {
	t.Helper()
	switch databaseType {
	case "postgres":
		container, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase("app"), postgres.WithUsername("app"), postgres.WithPassword("app"), postgres.BasicWaitStrategies(), postgres.WithSQLDriver("pgx"))
		if err != nil {
			t.Fatal(err)
		}
		testcontainers.CleanupContainer(t, container)
		dsn, err := container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			t.Fatal(err)
		}
		return dsn, dsn
	case "mysql":
		container, err := mysql.Run(ctx, "mysql:8.4", mysql.WithDatabase("app"), mysql.WithUsername("app"), mysql.WithPassword("app"))
		if err != nil {
			t.Fatal(err)
		}
		testcontainers.CleanupContainer(t, container)
		dsn, err := container.ConnectionString(ctx, "parseTime=true")
		if err != nil {
			t.Fatal(err)
		}
		migrationDSN, err := container.ConnectionString(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return dsn, "mysql://" + migrationDSN
	default:
		t.Fatal(fmt.Errorf("unsupported database %q", databaseType))
		return "", ""
	}
}
