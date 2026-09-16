package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/lihongjie0209/go-api-template/internal/buildinfo"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/migration"
)

func main() {
	configPath := flag.String("config", "", "configuration file path (default: discover config.* in . or ./config)")
	profile := flag.String("env", "", "active environment profile (overrides APP_ENV and config)")
	direction := flag.String("direction", "up", "migration direction: up or down")
	steps := flag.Int("steps", 0, "number of steps; negative values migrate down")
	showVersion := flag.Bool("version", false, "print build version information and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("version=%s commit=%s build_time=%s\n", buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime)
		return
	}
	if *direction != "up" && *direction != "down" {
		fmt.Fprintln(os.Stderr, "direction must be up or down")
		os.Exit(2)
	}
	cfg, err := config.LoadMigrationWithProfile(*configPath, *profile)
	if err == nil {
		err = migration.Run(cfg, *direction, *steps)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}
}
