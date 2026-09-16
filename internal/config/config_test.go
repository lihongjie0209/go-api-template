package config

import (
	"math"
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
	if cfg.EventBus.ConsumerAckWait != 30*time.Second || cfg.EventBus.ConsumerHandlerTimeout != 25*time.Second {
		t.Fatalf("unexpected event consumer deadlines: %+v", cfg.EventBus)
	}
	if cfg.EventBus.DispatchInterval != time.Second || cfg.EventBus.DispatchBatchSize != 100 || cfg.EventBus.DispatchLease != 30*time.Second || cfg.EventBus.DispatchRetryDelay != 2*time.Second {
		t.Fatalf("unexpected outbox dispatch defaults: %+v", cfg.EventBus)
	}
}

func TestConfig_ValidateJWTAsymmetricKey(t *testing.T) {
	t.Parallel()
	cfg := validDevelopmentConfig(t)
	cfg.JWT = JWT{Algorithm: "RS256", KeyID: "missing-private-key"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "configured together") {
		t.Fatalf("Validate() error = %v, want asymmetric key pairing error", err)
	}
}

func TestConfig_ValidateAuthorizationDependency(t *testing.T) {
	t.Parallel()
	cfg := validDevelopmentConfig(t)
	cfg.Authorization.Enabled = true
	delete(cfg.Outbound.GRPC, "authorization")
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "outbound.grpc.authorization") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestConfig_RejectsUnboundedAuthorizationRefresh(t *testing.T) {
	t.Parallel()
	for _, interval := range []time.Duration{time.Millisecond, 11 * time.Minute} {
		cfg := validDevelopmentConfig(t)
		cfg.Authorization.Enabled = true
		cfg.Authorization.PolicyRefreshInterval = interval
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "between 100ms and 10m") {
			t.Fatalf("interval %s Validate() error = %v", interval, err)
		}
	}
}

func TestValidateClientPolicy_PlaintextCredentialsRequireExplicitNonProductionOptIn(t *testing.T) {
	retry := Retry{MaxAttempts: 1, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond}
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

func TestConfig_RejectsUnsafeOutboundClientConfiguration(t *testing.T) {
	t.Parallel()
	validHTTP := func() HTTPUpstream {
		return HTTPUpstream{
			BaseURL: "https://billing.example.com",
			Timeout: time.Second,
			Retry:   Retry{MaxAttempts: 1, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond},
		}
	}
	tests := []struct {
		name   string
		mutate func(*HTTPUpstream)
		want   string
	}{
		{name: "URL credentials", mutate: func(upstream *HTTPUpstream) { upstream.BaseURL = "https://user:secret@billing.example.com" }, want: "without credentials"},
		{name: "URL query", mutate: func(upstream *HTTPUpstream) { upstream.BaseURL = "https://billing.example.com?token=secret" }, want: "without credentials"},
		{name: "short timeout", mutate: func(upstream *HTTPUpstream) { upstream.Timeout = time.Millisecond }, want: "between 10ms and 5m"},
		{name: "HTTP retry methods", mutate: func(upstream *HTTPUpstream) { upstream.Retry.Methods = []string{"/billing.v1.Billing/Get"} }, want: "gRPC-only"},
		{name: "token without type", mutate: func(upstream *HTTPUpstream) { upstream.Auth.Token = "secret" }, want: "auth.type is required"},
		{name: "oversized token", mutate: func(upstream *HTTPUpstream) {
			upstream.Auth = ClientAuth{Type: "bearer", Token: strings.Repeat("x", 8193)}
			upstream.TLS.Enabled = true
		}, want: "auth.token is required"},
		{name: "disabled TLS settings", mutate: func(upstream *HTTPUpstream) { upstream.TLS.CAFile = "ca.pem" }, want: "require tls.enabled"},
		{name: "unbounded breaker", mutate: func(upstream *HTTPUpstream) {
			upstream.Breaker = Breaker{Enabled: true, FailureThreshold: 10001, OpenTimeout: time.Second}
		}, want: "breaker policy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validDevelopmentConfig(t)
			upstream := validHTTP()
			test.mutate(&upstream)
			cfg.Outbound.HTTP = map[string]HTTPUpstream{"billing": upstream}
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
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
	if cfg.Redis.Address != "127.0.0.1:6379" || cfg.Redis.DB != 0 || cfg.Redis.DialTimeout != 5*time.Second || cfg.Redis.ReadTimeout != 3*time.Second || cfg.Redis.WriteTimeout != 3*time.Second {
		t.Fatalf("Redis = %+v, want mandatory localhost defaults", cfg.Redis)
	}
	if len(cfg.Runtime.ConfigFiles) != 0 {
		t.Fatalf("ConfigFiles = %v, want no loaded files", cfg.Runtime.ConfigFiles)
	}
}

func TestConfig_ValidatePSKLength(t *testing.T) {
	t.Parallel()
	cfg := validDevelopmentConfig(t)
	cfg.Auth.PSK = PSK{Enabled: true, Key: "short"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "at least 32 bytes") {
		t.Fatalf("Validate() error = %v, want short psk error", err)
	}
}

func TestConfig_ValidateAutoMigration(t *testing.T) {
	t.Parallel()
	cfg := validDevelopmentConfig(t)
	cfg.Migration.AutoUp = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "migration.auto_up") {
		t.Fatalf("Validate() error = %v, want auto migration dependency error", err)
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
		{
			name: "timeout reaches interval",
			content: "database:\n  enabled: true\n  type: postgres\n  dsn: postgres://localhost/app\n" +
				"data_lifecycle:\n  enabled: true\n  interval: 5m\n  timeout: 5m\n",
			want: "timeout",
		},
		{
			name: "unbounded purge batches",
			content: "database:\n  enabled: true\n  type: postgres\n  dsn: postgres://localhost/app\n" +
				"data_lifecycle:\n  enabled: true\n  purge_max_batches: 101\n",
			want: "purge_max_batches",
		},
		{
			name: "dead outbox retention shorter than published",
			content: "database:\n  enabled: true\n  type: postgres\n  dsn: postgres://localhost/app\n" +
				"data_lifecycle:\n  enabled: true\n  outbox_published_retention: 168h\n  outbox_dead_retention: 24h\n",
			want: "outbox dead retention",
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

func TestConfigRejectsUnsafeCronBounds(t *testing.T) {
	t.Parallel()
	for _, mutate := range []func(*Cron){
		func(cron *Cron) { cron.Timezone = strings.Repeat("x", 101) },
		func(cron *Cron) { cron.SampleSpec = strings.Repeat("*", 257) },
		func(cron *Cron) { cron.JobTimeout = time.Hour + time.Second },
	} {
		cfg := validDevelopmentConfig(t)
		mutate(&cfg.Cron)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "cron requires") {
			t.Fatalf("Validate() error = %v", err)
		}
	}
}

func TestLoad_RejectsMissingExplicitFileAndInvalidProfile(t *testing.T) {
	t.Parallel()
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("Load(missing) error = %v", err)
	}
	if _, err := LoadWithProfile("../../config/config.yaml", "../production"); err == nil || !strings.Contains(err.Error(), "invalid environment profile") {
		t.Fatalf("LoadWithProfile(invalid) error = %v", err)
	}
}

func TestLoad_RejectsInvalidLogSettings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "level", content: "log:\n  level: verbose\n", want: "log.level"},
		{name: "format", content: "log:\n  format: xml\n", want: "log.format"},
		{name: "file", content: "log:\n  file: ''\n", want: "rotation settings"},
		{name: "size", content: "log:\n  max_size_mb: 0\n", want: "rotation settings"},
		{name: "backups", content: "log:\n  max_backups: 0\n", want: "rotation settings"},
		{name: "age", content: "log:\n  max_age_days: 0\n", want: "rotation settings"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(configPath, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(configPath); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestConfig_RejectsInvalidDatabasePoolSettings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Database)
	}{
		{name: "open connections", mutate: func(cfg *Database) { cfg.MaxOpenConns = 0 }},
		{name: "idle exceeds open", mutate: func(cfg *Database) { cfg.MaxIdleConns = cfg.MaxOpenConns + 1 }},
		{name: "connection lifetime", mutate: func(cfg *Database) { cfg.ConnMaxLifetime = 0 }},
		{name: "connection idle time", mutate: func(cfg *Database) { cfg.ConnMaxIdleTime = 0 }},
		{name: "ping timeout", mutate: func(cfg *Database) { cfg.PingTimeout = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validDevelopmentConfig(t)
			cfg.Database.Enabled = true
			cfg.Database.DSN = "postgres://app:secret@localhost/app"
			test.mutate(&cfg.Database)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "bounded pool sizes") {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestConfig_RejectsInvalidHTTPBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*HTTP)
	}{
		{name: "read timeout", mutate: func(cfg *HTTP) { cfg.ReadTimeout = 0 }},
		{name: "write timeout", mutate: func(cfg *HTTP) { cfg.WriteTimeout = 0 }},
		{name: "idle timeout", mutate: func(cfg *HTTP) { cfg.IdleTimeout = 0 }},
		{name: "request timeout", mutate: func(cfg *HTTP) { cfg.RequestTimeout = 0 }},
		{name: "body size", mutate: func(cfg *HTTP) { cfg.MaxBodyBytes = 0 }},
		{name: "unbounded body size", mutate: func(cfg *HTTP) { cfg.MaxBodyBytes = 1<<30 + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validDevelopmentConfig(t)
			test.mutate(&cfg.HTTP)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "http requires positive timeouts") {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestConfig_RejectsUnsafeOutboxBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*EventBus)
	}{
		{name: "delivery attempts", mutate: func(cfg *EventBus) { cfg.ConsumerMaxDeliver = 101 }},
		{name: "handler reaches ack deadline", mutate: func(cfg *EventBus) { cfg.ConsumerHandlerTimeout = cfg.ConsumerAckWait }},
		{name: "dispatch interval", mutate: func(cfg *EventBus) { cfg.DispatchInterval = time.Millisecond }},
		{name: "batch size", mutate: func(cfg *EventBus) { cfg.DispatchBatchSize = 1001 }},
		{name: "lease shorter than publish", mutate: func(cfg *EventBus) { cfg.DispatchLease = cfg.PublishTimeout }},
		{name: "retry delay", mutate: func(cfg *EventBus) { cfg.DispatchRetryDelay = 2 * time.Hour }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validDevelopmentConfig(t)
			cfg.EventBus.Enabled = true
			test.mutate(&cfg.EventBus)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "dispatch settings") {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestConfig_RejectsUnsafeRateLimitBounds(t *testing.T) {
	t.Parallel()
	for _, rule := range []RateLimitRule{
		{Rate: 1_000_001, Burst: 1, Period: time.Minute},
		{Rate: 1, Burst: 1_000_001, Period: time.Minute},
		{Rate: 1, Burst: 1, Period: time.Millisecond},
		{Rate: 1, Burst: 1, Period: 25 * time.Hour},
	} {
		cfg := validDevelopmentConfig(t)
		cfg.RateLimit.Enabled = true
		cfg.RateLimit.IP = rule
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "bounded and positive") {
			t.Fatalf("rule %+v Validate() error = %v", rule, err)
		}
	}
}

func TestConfig_RejectsUnsafeRedisTimeouts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Redis)
	}{
		{name: "address", mutate: func(redis *Redis) { redis.Address = "" }},
		{name: "dial timeout", mutate: func(redis *Redis) { redis.DialTimeout = 0 }},
		{name: "read timeout", mutate: func(redis *Redis) { redis.ReadTimeout = 0 }},
		{name: "write timeout", mutate: func(redis *Redis) { redis.WriteTimeout = 0 }},
		{name: "unbounded timeout", mutate: func(redis *Redis) { redis.ReadTimeout = time.Minute + time.Second }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validDevelopmentConfig(t)
			test.mutate(&cfg.Redis)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "positive timeouts") {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestConfig_RejectsUnsafeHealthTimeouts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Health)
	}{
		{name: "database zero", mutate: func(health *Health) { health.DatabaseTimeout = 0 }},
		{name: "database unbounded", mutate: func(health *Health) { health.DatabaseTimeout = 30*time.Second + time.Millisecond }},
		{name: "redis zero", mutate: func(health *Health) { health.RedisTimeout = 0 }},
		{name: "redis unbounded", mutate: func(health *Health) { health.RedisTimeout = 30*time.Second + time.Millisecond }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validDevelopmentConfig(t)
			test.mutate(&cfg.Health)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "health dependency timeouts") {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestConfig_RejectsUnsafeTracingEndpoint(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{"collector:4318", "ftp://collector:4318", "https://user:secret@collector:4318", "https://collector:4318?token=secret"} {
		t.Run(endpoint, func(t *testing.T) {
			cfg := validDevelopmentConfig(t)
			cfg.Observability.TracingEnabled = true
			cfg.Observability.TracingEndpoint = endpoint
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "absolute HTTP(S) URL") {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestConfig_RejectsUnsafeObservabilityBounds(t *testing.T) {
	t.Parallel()
	for _, ratio := range []float64{math.NaN(), math.Inf(1), -0.1, 1.1} {
		cfg := validDevelopmentConfig(t)
		cfg.Observability.TracingSampleRatio = ratio
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "sample_ratio") {
			t.Fatalf("ratio %v Validate() error = %v", ratio, err)
		}
	}
	cfg := validDevelopmentConfig(t)
	cfg.Observability.PprofEnabled = true
	cfg.Observability.PprofToken = strings.Repeat("p", 4097)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "32-4096") {
		t.Fatalf("oversized pprof token Validate() error = %v", err)
	}
}

func TestConfig_RejectsUnboundedOutboundMetricLabels(t *testing.T) {
	t.Parallel()
	cfg := validDevelopmentConfig(t)
	cfg.Outbound.HTTP = map[string]HTTPUpstream{strings.Repeat("x", 64): {BaseURL: "https://example.com", Timeout: time.Second}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "bounded lowercase identifier") {
		t.Fatalf("invalid HTTP client name Validate() error = %v", err)
	}
}

func TestConfig_RejectsUnboundedCacheTTLs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "user", mutate: func(cfg *Config) { cfg.User.CacheTTL = 24*time.Hour + time.Second }},
		{name: "tenant", mutate: func(cfg *Config) { cfg.Tenant.CacheTTL = 24*time.Hour + time.Second }},
		{name: "menu", mutate: func(cfg *Config) { cfg.Menu.CacheTTL = 24*time.Hour + time.Second }},
		{name: "platform config", mutate: func(cfg *Config) { cfg.PlatformConfig.CacheTTL = 24*time.Hour + time.Second }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validDevelopmentConfig(t)
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "24h") {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestConfig_RejectsUnsafeDistributedLockBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*DistributedLock)
	}{
		{name: "short ttl", mutate: func(lock *DistributedLock) { lock.TTL = 299 * time.Millisecond }},
		{name: "long ttl", mutate: func(lock *DistributedLock) { lock.TTL = 5*time.Minute + time.Millisecond }},
		{name: "short retry", mutate: func(lock *DistributedLock) { lock.RetryDelay = 9 * time.Millisecond }},
		{name: "long retry", mutate: func(lock *DistributedLock) { lock.RetryDelay = 5*time.Second + time.Millisecond }},
		{name: "retry not below ttl", mutate: func(lock *DistributedLock) { lock.RetryDelay = lock.TTL }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validDevelopmentConfig(t)
			test.mutate(&cfg.DistributedLock)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "distributed_lock") {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestDefaultCORSAllowsIdempotencyKey(t *testing.T) {
	t.Parallel()
	cfg := validDevelopmentConfig(t)
	found := false
	for _, header := range cfg.HTTP.CORS.AllowedHeaders {
		found = found || strings.EqualFold(header, "Idempotency-Key")
	}
	if !found {
		t.Fatalf("allowed headers = %v", cfg.HTTP.CORS.AllowedHeaders)
	}
}

func TestLoad_ShippedDevelopmentAndTestProfiles(t *testing.T) {
	t.Parallel()
	for _, profile := range []string{"development", "test"} {
		t.Run(profile, func(t *testing.T) {
			cfg, err := LoadWithProfile("../../config/config.yaml", profile)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Runtime.ActiveProfile != profile || cfg.App.Env != profile || len(cfg.Runtime.ConfigFiles) != 2 {
				t.Fatalf("runtime=%+v app.env=%q", cfg.Runtime, cfg.App.Env)
			}
		})
	}
}

func TestLoad_ShippedProductionProfileRequiresAndAcceptsInjectedSecrets(t *testing.T) {
	t.Setenv("APP_GRPC_TLS_CERT_FILE", "/run/secrets/tls.crt")
	t.Setenv("APP_GRPC_TLS_KEY_FILE", "/run/secrets/tls.key")
	t.Setenv("APP_DATABASE_DSN", "postgres://app:secret@postgres:5432/app?sslmode=require")
	t.Setenv("APP_MIGRATION_DATABASE_URL", "postgres://app:secret@postgres:5432/app?sslmode=require")
	t.Setenv("APP_AUTH_JWKS_URL", "https://identity.example.com/.well-known/jwks.json")
	t.Setenv("APP_JWT_KEY_ID", "production-1")
	t.Setenv("APP_JWT_PRIVATE_KEY_FILE", "/run/secrets/jwt.pem")
	t.Setenv("APP_SECURITY_LOG_HASH_KEY", strings.Repeat("h", 32))

	cfg, err := LoadWithProfile("../../config/config.yaml", "production")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EventBus.Enabled || !cfg.OperationLog.Enabled || !cfg.SecurityLog.Enabled || cfg.Runtime.ActiveProfile != "production" {
		t.Fatalf("production audit chain/runtime = %+v/%+v", cfg.EventBus, cfg.Runtime)
	}
}

func TestLoadMigrationWithProfile_ProductionDoesNotRequireAPISecrets(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	content := "app:\n  env: production\ndatabase:\n  name: orders_db\n  schema: orders\nmigration:\n  path: /app/migrations/postgres\n  database_url: postgres://app:secret@postgres:5432/orders_db?sslmode=require\n  table: orders_schema_migrations\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadMigrationWithProfile(path, "production")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseName != "orders_db" || cfg.Schema != "orders" || cfg.Table != "orders_schema_migrations" {
		t.Fatalf("migration config = %+v", cfg)
	}
}

func TestLoadDatabaseWithProfile_ProductionDoesNotRequireAPISecrets(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	content := "app:\n  env: production\ndatabase:\n  enabled: true\n  type: postgres\n  name: orders_db\n  schema: orders\n  dsn: postgres://app:secret@postgres:5432/orders_db?sslmode=require\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadDatabaseWithProfile(path, "production")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Name != "orders_db" || cfg.Database.Schema != "orders" || cfg.Database.Type != "postgres" {
		t.Fatalf("database config = %+v", cfg.Database)
	}
}

func TestLoadMigrationWithProfile_RejectsInvalidMigrationIdentity(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	content := "database:\n  name: orders_db\n  schema: orders\nmigration:\n  path: migrations/postgres\n  database_url: postgres://localhost/orders_db\n  table: shared-table\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadMigrationWithProfile(path, "development"); err == nil || !strings.Contains(err.Error(), "migration.table") {
		t.Fatalf("LoadMigrationWithProfile() error = %v, want migration.table validation", err)
	}
}

func TestConfig_ProductionSecurityRequirementsFailClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		want   string
		mutate func(*Config)
	}{
		{name: "grpc tls", want: "grpc tls must be enabled", mutate: func(cfg *Config) { cfg.GRPC.TLS.Enabled = false }},
		{name: "grpc reflection", want: "grpc reflection must be disabled", mutate: func(cfg *Config) { cfg.GRPC.ReflectionEnabled = true }},
		{name: "identity verifier", want: "production authentication requires identity JWKS", mutate: func(cfg *Config) { cfg.Auth.JWKSURL = "" }},
		{name: "authorization", want: "authorization must be enabled", mutate: func(cfg *Config) { cfg.Authorization.Enabled = false }},
		{name: "jwt signing key", want: "asymmetric JWT signing key", mutate: func(cfg *Config) { cfg.JWT.KeyID, cfg.JWT.PrivateKey = "", "" }},
		{name: "durable security logs", want: "event_bus, operation_log, and security_log", mutate: func(cfg *Config) { cfg.SecurityLog.Enabled = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validProductionConfig(t)
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func validProductionConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := LoadWithProfile("../../config/config.yaml", "development")
	if err != nil {
		t.Fatal(err)
	}
	cfg.App.Env = "production"
	cfg.GRPC.ReflectionEnabled = false
	cfg.GRPC.TLS = GRPCTLS{Enabled: true, CertFile: "tls.crt", KeyFile: "tls.key"}
	cfg.Swagger.Enabled = false
	cfg.Swagger.RequireAuth = true
	cfg.Auth.JWKSURL = "https://identity.example.com/.well-known/jwks.json"
	cfg.Auth.Issuer = "identity-service"
	cfg.Auth.Audience = "test-service"
	cfg.Authorization.Enabled = true
	cfg.JWT.KeyID = "production-1"
	cfg.JWT.PrivateKey = "configured-by-secret-manager"
	cfg.Database.Enabled = true
	cfg.Database.DSN = "postgres://app:secret@postgres:5432/app?sslmode=require"
	cfg.EventBus.Enabled = true
	cfg.OperationLog.Enabled = true
	cfg.SecurityLog.Enabled = true
	cfg.SecurityLog.HashKey = strings.Repeat("h", 32)
	return cfg
}

func validDevelopmentConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := LoadWithProfile("../../config/config.yaml", "development")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestConfig_RedisKeyPrefixAlwaysIncludesActiveEnvironment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "default service namespace",
			cfg:  Config{App: App{Name: "orders", Env: "development"}},
			want: "development:orders:",
		},
		{
			name: "runtime profile takes precedence",
			cfg:  Config{Runtime: Runtime{ActiveProfile: "test"}, App: App{Name: "orders", Env: "development"}},
			want: "test:orders:",
		},
		{
			name: "explicit namespace retains environment",
			cfg:  Config{App: App{Env: "production"}, Redis: Redis{KeyPrefix: "payments:"}},
			want: "production:payments:",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.cfg.RedisKeyPrefix(); got != test.want {
				t.Fatalf("RedisKeyPrefix() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestConfigRejectsUnsafeIdempotencyRoutes(t *testing.T) {
	t.Parallel()
	for _, route := range []string{
		"/api/v1/example/ping",
		"/api/v1/auth/login",
		"/api/v1/auth/refresh",
		"/api/v1/service-accounts/secret/rotate",
		"/api/v1/files/download",
		"/api/v1/platform-configs/update",
	} {
		t.Run(route, func(t *testing.T) {
			cfg := validDevelopmentConfig(t)
			cfg.Idempotency.Enabled = true
			cfg.Idempotency.HTTPPaths = []string{route}
			cfg.Idempotency.GRPCMethods = nil
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "forbidden query or sensitive-response route") {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestConfigRejectsUnsafeIdempotencyTTL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "processing does not cover request timeout", mutate: func(cfg *Config) { cfg.Idempotency.ProcessingTTL = cfg.HTTP.RequestTimeout }},
		{name: "processing lease is excessive", mutate: func(cfg *Config) { cfg.Idempotency.ProcessingTTL = 16 * time.Minute }},
		{name: "result retention is excessive", mutate: func(cfg *Config) { cfg.Idempotency.ResultTTL = 8 * 24 * time.Hour }},
		{name: "failure retention is excessive", mutate: func(cfg *Config) { cfg.Idempotency.FailureTTL = 25 * time.Hour }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validDevelopmentConfig(t)
			cfg.Idempotency.Enabled = true
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "enabled idempotency") {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestConfigRejectsUnboundedOperationLogPayload(t *testing.T) {
	t.Parallel()
	for _, size := range []int{255, 64<<10 + 1} {
		cfg := validProductionConfig(t)
		cfg.OperationLog.MaxPayloadBytes = size
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "max_payload_bytes") {
			t.Fatalf("size=%d Validate() error = %v", size, err)
		}
	}
}

func TestConfigRejectsUnsafeSecurityLogSettings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "small payload", mutate: func(cfg *Config) { cfg.SecurityLog.MaxPayloadBytes = 255 }, want: "max_payload_bytes"},
		{name: "large payload", mutate: func(cfg *Config) { cfg.SecurityLog.MaxPayloadBytes = 64<<10 + 1 }, want: "max_payload_bytes"},
		{name: "oversized hash key", mutate: func(cfg *Config) { cfg.SecurityLog.HashKey = strings.Repeat("h", 4097) }, want: "hash_key"},
		{name: "production fail open", mutate: func(cfg *Config) { cfg.SecurityLog.FailClosed = false }, want: "fail closed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validProductionConfig(t)
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestConfigRejectsUnsafeObjectStorageAndFileSettings(t *testing.T) {
	t.Parallel()
	base := func(t *testing.T) Config {
		cfg := validProductionConfig(t)
		cfg.ObjectStorage = ObjectStorage{Enabled: true, Provider: "s3", Bucket: "files", Region: "cn-test-1", PresignTTL: time.Minute, Timeout: 30 * time.Second}
		return cfg
	}
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "plaintext production endpoint", mutate: func(cfg *Config) { cfg.ObjectStorage.Endpoint = "http://minio:9000" }, want: "must use https"},
		{name: "endpoint credentials", mutate: func(cfg *Config) { cfg.ObjectStorage.Endpoint = "https://user:pass@storage.example.com" }, want: "without credentials"},
		{name: "S3 CNAME mode", mutate: func(cfg *Config) { cfg.ObjectStorage.UseCName = true }, want: "provider-compatible"},
		{name: "unbounded provider timeout", mutate: func(cfg *Config) { cfg.ObjectStorage.Timeout = 5*time.Minute + time.Millisecond }, want: "object_storage requires"},
		{name: "invalid allowed media type", mutate: func(cfg *Config) { cfg.Files.Enabled = true; cfg.Files.AllowedTypes = []string{"not a media type"} }, want: "allowed_types"},
		{name: "upload intent reclaimed too early", mutate: func(cfg *Config) { cfg.Files.Enabled = true; cfg.Files.UploadStaleAfter = time.Second }, want: "upload recovery"},
		{name: "upload intent retained too long", mutate: func(cfg *Config) { cfg.Files.Enabled = true; cfg.Files.UploadStaleAfter = 24*time.Hour + time.Second }, want: "upload recovery"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := base(t)
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}
