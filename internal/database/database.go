package database

import (
	"context"
	"fmt"

	"github.com/lihongjie0209/go-api-template/internal/config"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

func Open(ctx context.Context, cfg config.Database) (*sqlx.DB, error) {
	driver, err := driverName(cfg.Type)
	if err != nil {
		return nil, err
	}
	db, err := sqlx.Open(driver, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	pingCtx, cancel := context.WithTimeout(ctx, cfg.PingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}

func driverName(dbType string) (string, error) {
	switch dbType {
	case "mysql":
		return "mysql", nil
	case "postgres", "kingbase":
		return "pgx", nil
	default:
		return "", fmt.Errorf("unsupported database type %q", dbType)
	}
}
