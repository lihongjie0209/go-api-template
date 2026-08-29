package migration

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func Run(cfg config.Migration, direction string, steps int) (runErr error) {
	if cfg.DatabaseURL == "" {
		return errors.New("migration.database_url is required")
	}
	absPath, err := filepath.Abs(cfg.Path)
	if err != nil {
		return fmt.Errorf("resolve migration path: %w", err)
	}
	source := (&url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}).String()
	m, err := migrate.New(source, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	defer func() {
		sourceErr, databaseErr := m.Close()
		if sourceErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close migration source: %w", sourceErr))
		}
		if databaseErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close migration database: %w", databaseErr))
		}
	}()
	if steps != 0 {
		err = m.Steps(steps)
	} else if direction == "down" {
		err = m.Down()
	} else {
		err = m.Up()
	}
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("run migration: %w", err)
	}
	return runErr
}
