package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoad_EnvironmentOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("http:\n  address: 127.0.0.1:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_HTTP_ADDRESS", "127.0.0.1:9090")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTP.Address != "127.0.0.1:9090" {
		t.Fatalf("HTTP.Address = %q, want %q", cfg.HTTP.Address, "127.0.0.1:9090")
	}
}

func TestLoad_EnvironmentStringSlicesAcceptBracketedLists(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("http:\n  address: 127.0.0.1:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(
		"APP_AUTH_PSK_GRPC_METHODS",
		"[/platform.export.v1.ExportProviderService/*, /platform.import.v1.ImportProviderService/*]",
	)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{
		"/platform.export.v1.ExportProviderService/*",
		"/platform.import.v1.ImportProviderService/*",
	}
	if len(cfg.Auth.PSK.GRPCMethods) != len(want) {
		t.Fatalf("GRPCMethods = %#v", cfg.Auth.PSK.GRPCMethods)
	}
	for index := range want {
		if cfg.Auth.PSK.GRPCMethods[index] != want[index] {
			t.Fatalf("GRPCMethods[%d] = %q, want %q", index, cfg.Auth.PSK.GRPCMethods[index], want[index])
		}
	}
}

func TestLoad_IdempotencyRouteListsCanBeOverriddenByEnvironment(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("idempotency:\n  http_paths: []\n  grpc_methods: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_IDEMPOTENCY_HTTP_PATHS", "[/api/v1/orders/create, /api/v1/orders/retry]")
	t.Setenv("APP_IDEMPOTENCY_GRPC_METHODS", "[/orders.v1.OrderService/Create]")
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Idempotency.HTTPPaths) != 2 || cfg.Idempotency.HTTPPaths[1] != "/api/v1/orders/retry" {
		t.Fatalf("HTTPPaths = %#v", cfg.Idempotency.HTTPPaths)
	}
	if len(cfg.Idempotency.GRPCMethods) != 1 || cfg.Idempotency.GRPCMethods[0] != "/orders.v1.OrderService/Create" {
		t.Fatalf("GRPCMethods = %#v", cfg.Idempotency.GRPCMethods)
	}
}

func TestLoad_UsesCanonicalPlatformEventStreamDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("http:\n  address: 127.0.0.1:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EventBus.StreamName != "PLATFORM_EVENTS" || len(cfg.EventBus.Subjects) != 1 || cfg.EventBus.Subjects[0] != "platform.>" {
		t.Fatalf("unexpected event stream defaults: %q %#v", cfg.EventBus.StreamName, cfg.EventBus.Subjects)
	}
	if cfg.EventBus.DispatchInterval != time.Second || cfg.EventBus.DispatchBatchSize != 100 || cfg.EventBus.DispatchLease != 30*time.Second || cfg.EventBus.DispatchRetryDelay != 2*time.Second {
		t.Fatalf("unexpected outbox dispatch defaults: %+v", cfg.EventBus)
	}
}

func TestConfig_ValidateJWTSecret(t *testing.T) {
	t.Parallel()
	cfg := Config{HTTP: HTTP{Address: "127.0.0.1:8080"}, Auth: Auth{ClientID: "client", ClientSecret: "secret"}, JWT: JWT{Secret: "short"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}

func TestConfig_ValidateAuthorizationDependency(t *testing.T) {
	t.Parallel()
	cfg := Config{
		HTTP:          HTTP{Address: "127.0.0.1:8080", RequestTimeout: time.Second},
		Database:      Database{Name: "go_api_template_db"},
		Health:        Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second},
		User:          User{CacheTTL: time.Second, LockTTL: time.Second, LockRetryDelay: time.Millisecond},
		Authorization: Authorization{Enabled: true},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "outbound.grpc.authorization") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateClientPolicy_PlaintextCredentialsRequireExplicitNonProductionOptIn(t *testing.T) {
	retry := Retry{MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
	auth := ClientAuth{Type: "psk", Token: strings.Repeat("p", 32)}
	if err := validateClientPolicy("application", auth, retry, Breaker{}, ClientTLS{}, false); err == nil || !strings.Contains(err.Error(), "allow_insecure") {
		t.Fatalf("validateClientPolicy() error = %v", err)
	}
	insecureTLS := ClientTLS{AllowInsecure: true}
	if err := validateClientPolicy("application", auth, retry, Breaker{}, insecureTLS, false); err != nil {
		t.Fatalf("validateClientPolicy() development error = %v", err)
	}
	if err := validateClientPolicy("application", auth, retry, Breaker{}, insecureTLS, true); err == nil || !strings.Contains(err.Error(), "production") {
		t.Fatalf("validateClientPolicy() production error = %v", err)
	}
}

func TestLoadWithProfile_MergesProfileThenEnvironment(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "config.yaml")
	profile := filepath.Join(dir, "config-test.yaml")
	if err := os.WriteFile(base, []byte("app:\n  env: development\nlog:\n  level: info\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile, []byte("log:\n  level: debug\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_LOG_LEVEL", "error")
	cfg, err := LoadWithProfile(base, "test")
	if err != nil {
		t.Fatalf("LoadWithProfile() error = %v", err)
	}
	if cfg.App.Env != "test" || cfg.Runtime.ActiveProfile != "test" {
		t.Fatalf("active profile = %q/%q", cfg.App.Env, cfg.Runtime.ActiveProfile)
	}
	if cfg.Log.Level != "error" {
		t.Fatalf("Log.Level = %q, want environment override", cfg.Log.Level)
	}
	if len(cfg.Runtime.ConfigFiles) != 2 || cfg.Runtime.ConfigFiles[1] != profile {
		t.Fatalf("ConfigFiles = %v", cfg.Runtime.ConfigFiles)
	}
}

func TestConfig_ValidateAuthSkipPattern(t *testing.T) {
	t.Parallel()
	cfg := Config{HTTP: HTTP{Address: "127.0.0.1:8080", RequestTimeout: time.Second}, Health: Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second}, User: User{CacheTTL: time.Second, LockTTL: time.Second, LockRetryDelay: time.Millisecond}, Auth: Auth{SkipHTTPPaths: []string{"/api/v1/[broken"}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want invalid wildcard error")
	}
}

func TestConfig_ValidateAutoMigration(t *testing.T) {
	t.Parallel()
	cfg := Config{
		HTTP:      HTTP{Address: "127.0.0.1:8080", RequestTimeout: time.Second},
		Health:    Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second},
		User:      User{CacheTTL: time.Second, LockTTL: time.Second, LockRetryDelay: time.Millisecond},
		Migration: Migration{AutoUp: true, Path: "migrations/postgres"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want auto migration dependency error")
	}
}
