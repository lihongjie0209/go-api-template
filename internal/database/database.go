package database

import (
	"context"
	"fmt"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
)

func Open(ctx context.Context, cfg config.Database) (*sqlx.DB, error) {
	driver, err := driverName(cfg.Type)
	if err != nil {
		return nil, err
	}
	var db *sqlx.DB
	if driver == "pgx" && (cfg.Name != "" || cfg.Schema != "") {
		connectionConfig, parseErr := pgx.ParseConfig(cfg.DSN)
		if parseErr != nil {
			return nil, fmt.Errorf("parse database dsn: %w", parseErr)
		}
		if cfg.Name != "" {
			connectionConfig.Database = cfg.Name
		}
		if cfg.Schema != "" {
			connectionConfig.RuntimeParams["search_path"] = cfg.Schema
		}
		db = sqlx.NewDb(stdlib.OpenDB(*connectionConfig), driver)
	} else if driver == "mysql" && cfg.Name != "" {
		connectionConfig, parseErr := mysqlDriver.ParseDSN(cfg.DSN)
		if parseErr != nil {
			return nil, fmt.Errorf("parse database dsn: %w", parseErr)
		}
		connectionConfig.DBName = cfg.Name
		db, err = sqlx.Open(driver, connectionConfig.FormatDSN())
		if err != nil {
			return nil, fmt.Errorf("open database: %w", err)
		}
	} else {
		db, err = sqlx.Open(driver, cfg.DSN)
		if err != nil {
			return nil, fmt.Errorf("open database: %w", err)
		}
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
