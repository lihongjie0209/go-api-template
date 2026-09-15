package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
)

func TestNew_DependencyGraph(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		App:  config.App{Name: "test", Env: "test", ShutdownTimeout: time.Second},
		HTTP: config.HTTP{Address: "127.0.0.1:0", ReadTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, MaxBodyBytes: 1024},
		Log:  config.Log{Level: "error", Format: "json", File: filepath.Join(t.TempDir(), "app.log"), MaxSizeMB: 1, MaxBackups: 1, MaxAgeDays: 1},
		JWT:  config.JWT{Issuer: "test", TTL: time.Hour},
		Cron: config.Cron{Timezone: "UTC"},
	}
	application := New(cfg)
	if err := application.Err(); err != nil {
		t.Fatalf("New() dependency graph error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := application.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}
