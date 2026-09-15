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

func TestLoad_AuthorizationRefreshIntervalCanBeOverridden(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("http:\n  address: 127.0.0.1:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_AUTHORIZATION_POLICY_REFRESH_INTERVAL", "5s")
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Authorization.PolicyRefreshInterval != 5*time.Second {
		t.Fatalf("PolicyRefreshInterval = %v", cfg.Authorization.PolicyRefreshInterval)
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

func TestConfig_ValidateJWTAsymmetricKey(t *testing.T) {
	t.Parallel()
	cfg := Config{HTTP: HTTP{Address: "127.0.0.1:8080"}, JWT: JWT{Algorithm: "RS256", KeyID: "missing-private-key"}}
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

func TestLoad_DiscoversCurrentDirectoryConfigAndProfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("app:\n  name: current-directory-service\nlog:\n  level: info\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config-test.yaml"), []byte("log:\n  level: debug\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("APP_ENV", "test")
	t.Setenv("APP_LOG_LEVEL", "error")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.App.Name != "current-directory-service" || cfg.App.Env != "test" {
		t.Fatalf("app = %+v", cfg.App)
	}
	if cfg.Log.Level != "error" {
		t.Fatalf("Log.Level = %q, want environment override", cfg.Log.Level)
	}
	if len(cfg.Runtime.ConfigFiles) != 2 {
		t.Fatalf("ConfigFiles = %v", cfg.Runtime.ConfigFiles)
	}
	for _, name := range []string{"config.yaml", "config-test.yaml"} {
		if !strings.HasSuffix(cfg.Runtime.ConfigFiles[0], name) && !strings.HasSuffix(cfg.Runtime.ConfigFiles[1], name) {
			t.Fatalf("ConfigFiles = %v, missing %s", cfg.Runtime.ConfigFiles, name)
		}
	}
}

func TestLoad_UsesDefaultsWhenCurrentDirectoryHasNoConfig(t *testing.T) {
	t.Chdir(t.TempDir())

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.App.Name != "go-api-template" || cfg.App.Env != "development" {
		t.Fatalf("app = %+v", cfg.App)
	}
	if len(cfg.Runtime.ConfigFiles) != 0 {
		t.Fatalf("ConfigFiles = %v, want no loaded files", cfg.Runtime.ConfigFiles)
	}
}

func TestConfig_ValidatePSKLength(t *testing.T) {
	t.Parallel()
	cfg := Config{HTTP: HTTP{Address: "127.0.0.1:8080", RequestTimeout: time.Second}, Health: Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second}, User: User{CacheTTL: time.Second, LockTTL: time.Second, LockRetryDelay: time.Millisecond}, Auth: Auth{PSK: PSK{Enabled: true, Key: "short"}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want short psk error")
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

func TestLoad_ValidatesDataLifecycleDatabaseAndBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "mysql archive unsupported",
			content: "database:\n  enabled: true\n  type: mysql\n  dsn: app:app@tcp(localhost:3306)/app\n" +
				"data_lifecycle:\n  enabled: true\n  archive_schema: audit_archive\n",
			want: "bounded purge batches",
		},
		{
			name: "premake unbounded",
			content: "database:\n  enabled: true\n  type: postgres\n  dsn: postgres://localhost/app\n" +
				"data_lifecycle:\n  enabled: true\n  premake_months: 25\n",
			want: "premake_months",
		},
		{
			name: "unsafe archive schema",
			content: "database:\n  enabled: true\n  type: postgres\n  dsn: postgres://localhost/app\n" +
				"data_lifecycle:\n  enabled: true\n  archive_schema: archive-invalid!\n",
			want: "archive_schema",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error=%v, want substring %q", err, test.want)
			}
		})
	}
}
