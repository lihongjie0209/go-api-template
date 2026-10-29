//go:build integration

package testutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/migration"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
)

// MySQLDatabase starts one isolated, migrated database for a package-local
// integration test. The caller owns no other business service or container.
func MySQLDatabase(t *testing.T) *sqlx.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(root, "go.mod")); statErr == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("locate repository root")
		}
		root = parent
	}
	container, err := mysql.Run(ctx, "mysql:8.4",
		mysql.WithDatabase("app"), mysql.WithUsername("app"), mysql.WithPassword("app"),
		mysql.WithConfigFile(filepath.Join(root, "integration", "testdata", "mysql.cnf")),
	)
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
	if err := migration.Run(config.Migration{
		Path:        filepath.Join(root, "migrations", "mysql"),
		DatabaseURL: "mysql://" + migrationDSN,
		Table:       "integration_audit_schema_migrations",
	}, "up", 0); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, config.Database{
		Type: "mysql", DSN: dsn, MaxOpenConns: 5, MaxIdleConns: 2,
		ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute, PingTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
