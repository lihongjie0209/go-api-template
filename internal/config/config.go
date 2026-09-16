package config

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

type Config struct {
	Runtime        Runtime        `mapstructure:"-"`
	App            App            `mapstructure:"app"`
	HTTP           HTTP           `mapstructure:"http"`
	GRPC           GRPC           `mapstructure:"grpc"`
	Log            Log            `mapstructure:"log"`
	Database       Database       `mapstructure:"database"`
	Redis          Redis          `mapstructure:"redis"`
	Health         Health         `mapstructure:"health"`
	RateLimit      RateLimit      `mapstructure:"rate_limit"`
	Observability  Observability  `mapstructure:"observability"`
	Swagger        Swagger        `mapstructure:"swagger"`
	JWT            JWT            `mapstructure:"jwt"`
	Auth           Auth           `mapstructure:"auth"`
	Authentication Authentication `mapstructure:"authentication"`
	Authorization  Authorization  `mapstructure:"authorization"`
	Cron           Cron           `mapstructure:"cron"`
	Migration      Migration      `mapstructure:"migration"`
	User           User           `mapstructure:"user"`
	Tenant         Tenant         `mapstructure:"tenant"`
	Menu           Menu           `mapstructure:"menu"`
	PlatformConfig PlatformConfig `mapstructure:"platform_config"`
	Idempotency    Idempotency    `mapstructure:"idempotency"`
	Outbound       Outbound       `mapstructure:"outbound"`
	EventBus       EventBus       `mapstructure:"event_bus"`
	ObjectStorage  ObjectStorage  `mapstructure:"object_storage"`
	Files          Files          `mapstructure:"files"`
	OperationLog   OperationLog   `mapstructure:"operation_log"`
	SecurityLog    SecurityLog    `mapstructure:"security_log"`
	DataLifecycle  DataLifecycle  `mapstructure:"data_lifecycle"`
}

type Runtime struct {
	ActiveProfile string   `json:"active_profile"`
	ConfigFiles   []string `json:"config_files"`
}

type App struct {
	Name            string        `mapstructure:"name"`
	Schema          string        `mapstructure:"schema"`
	Env             string        `mapstructure:"env"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
}
type HTTP struct {
	Address        string        `mapstructure:"address"`
	ReadTimeout    time.Duration `mapstructure:"read_timeout"`
	WriteTimeout   time.Duration `mapstructure:"write_timeout"`
	IdleTimeout    time.Duration `mapstructure:"idle_timeout"`
	RequestTimeout time.Duration `mapstructure:"request_timeout"`
	MaxBodyBytes   int64         `mapstructure:"max_body_bytes"`
	TrustedProxies []string      `mapstructure:"trusted_proxies"`
	CORS           CORS          `mapstructure:"cors"`
}

type CORS struct {
	Enabled        bool          `mapstructure:"enabled"`
	AllowedOrigins []string      `mapstructure:"allowed_origins"`
	AllowedHeaders []string      `mapstructure:"allowed_headers"`
	ExposedHeaders []string      `mapstructure:"exposed_headers"`
	MaxAge         time.Duration `mapstructure:"max_age"`
}
type GRPC struct {
	Enabled           bool    `mapstructure:"enabled"`
	Address           string  `mapstructure:"address"`
	ReflectionEnabled bool    `mapstructure:"reflection_enabled"`
	MaxReceiveBytes   int     `mapstructure:"max_receive_bytes"`
	TLS               GRPCTLS `mapstructure:"tls"`
}
type GRPCTLS struct {
	Enabled      bool   `mapstructure:"enabled"`
	CertFile     string `mapstructure:"cert_file"`
	KeyFile      string `mapstructure:"key_file"`
	ClientCAFile string `mapstructure:"client_ca_file"`
}
type Log struct {
	Level      string `mapstructure:"level"`
	Format     string `mapstructure:"format"`
	File       string `mapstructure:"file"`
	MaxSizeMB  int    `mapstructure:"max_size_mb"`
	MaxBackups int    `mapstructure:"max_backups"`
	MaxAgeDays int    `mapstructure:"max_age_days"`
	Compress   bool   `mapstructure:"compress"`
}
type Database struct {
	Enabled         bool          `mapstructure:"enabled"`
	Name            string        `mapstructure:"name"`
	Schema          string        `mapstructure:"schema"`
	Type            string        `mapstructure:"type"`
	DSN             string        `mapstructure:"dsn"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `mapstructure:"conn_max_idle_time"`
	PingTimeout     time.Duration `mapstructure:"ping_timeout"`
}
type Redis struct {
	Enabled      bool          `mapstructure:"enabled"`
	KeyPrefix    string        `mapstructure:"key_prefix"`
	Address      string        `mapstructure:"address"`
	Username     string        `mapstructure:"username"`
	Password     string        `mapstructure:"password"`
	DB           int           `mapstructure:"db"`
	DialTimeout  time.Duration `mapstructure:"dial_timeout"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
}

// RedisKeyPrefix returns the mandatory environment and service scoped Redis
// namespace. An explicit key_prefix replaces only the service portion; the
// active environment is always retained so deployments sharing Redis cannot
// affect one another.
func (c Config) RedisKeyPrefix() string {
	profile := strings.ToLower(strings.TrimSpace(c.Runtime.ActiveProfile))
	if profile == "" {
		profile = strings.ToLower(strings.TrimSpace(c.App.Env))
	}
	if profile == "" {
		profile = "development"
	}
	namespace := c.Redis.KeyPrefix
	if namespace == "" {
		namespace = strings.TrimSpace(c.App.Name) + ":"
	}
	return profile + ":" + namespace
}

type Health struct {
	DatabaseTimeout time.Duration `mapstructure:"database_timeout"`
	RedisTimeout    time.Duration `mapstructure:"redis_timeout"`
}
type RateLimit struct {
	Enabled  bool          `mapstructure:"enabled"`
	FailOpen bool          `mapstructure:"fail_open"`
	IP       RateLimitRule `mapstructure:"ip"`
	API      RateLimitRule `mapstructure:"api"`
	User     RateLimitRule `mapstructure:"user"`
	Login    RateLimitRule `mapstructure:"login"`
}
type RateLimitRule struct {
	Rate   int           `mapstructure:"rate"`
	Burst  int           `mapstructure:"burst"`
	Period time.Duration `mapstructure:"period"`
}
type Observability struct {
	MetricsEnabled     bool    `mapstructure:"metrics_enabled"`
	TracingEnabled     bool    `mapstructure:"tracing_enabled"`
	TracingEndpoint    string  `mapstructure:"tracing_endpoint"`
	TracingSampleRatio float64 `mapstructure:"tracing_sample_ratio"`
	PprofEnabled       bool    `mapstructure:"pprof_enabled"`
	PprofToken         string  `mapstructure:"pprof_token"`
}
type Swagger struct {
	Enabled     bool `mapstructure:"enabled"`
	RequireAuth bool `mapstructure:"require_auth"`
}
type JWT struct {
	Issuer         string               `mapstructure:"issuer"`
	Audience       string               `mapstructure:"audience"`
	Algorithm      string               `mapstructure:"algorithm"`
	KeyID          string               `mapstructure:"key_id"`
	PrivateKey     string               `mapstructure:"private_key"`
	PrivateKeyFile string               `mapstructure:"private_key_file"`
	PublicKeys     []JWTVerificationKey `mapstructure:"public_keys"`
	TTL            time.Duration        `mapstructure:"ttl"`
}
type JWTVerificationKey struct {
	KeyID         string `mapstructure:"key_id"`
	Algorithm     string `mapstructure:"algorithm"`
	PublicKey     string `mapstructure:"public_key"`
	PublicKeyFile string `mapstructure:"public_key_file"`
}
type Auth struct {
	JWKSURL  string `mapstructure:"jwks_url"`
	Issuer   string `mapstructure:"issuer"`
	Audience string `mapstructure:"audience"`
	PSK      PSK    `mapstructure:"psk"`
}
type Authorization struct {
	Enabled               bool          `mapstructure:"enabled"`
	PolicyRefreshInterval time.Duration `mapstructure:"policy_refresh_interval"`
}
type Authentication struct {
	RefreshTTL        time.Duration `mapstructure:"refresh_ttl"`
	MaxFailedAttempts int64         `mapstructure:"max_failed_attempts"`
	LockDuration      time.Duration `mapstructure:"lock_duration"`
}
type PSK struct {
	Enabled bool   `mapstructure:"enabled"`
	Key     string `mapstructure:"key"`
}
type Cron struct {
	Enabled    bool   `mapstructure:"enabled"`
	Timezone   string `mapstructure:"timezone"`
	SampleSpec string `mapstructure:"sample_spec"`
}
type Migration struct {
	AutoUp       bool   `mapstructure:"auto_up"`
	CreateSchema bool   `mapstructure:"create_schema"`
	Path         string `mapstructure:"path"`
	DatabaseURL  string `mapstructure:"database_url"`
	Table        string `mapstructure:"table"`
	Schema       string `mapstructure:"-"`
	DatabaseName string `mapstructure:"-"`
}
type User struct {
	CacheTTL       time.Duration `mapstructure:"cache_ttl"`
	LockTTL        time.Duration `mapstructure:"lock_ttl"`
	LockRetryDelay time.Duration `mapstructure:"lock_retry_delay"`
}
type Tenant struct {
	CacheTTL time.Duration `mapstructure:"cache_ttl"`
}
type Menu struct {
	CacheTTL time.Duration `mapstructure:"cache_ttl"`
	MaxNodes int           `mapstructure:"max_nodes"`
}
type PlatformConfig struct {
	CacheTTL time.Duration `mapstructure:"cache_ttl"`
}
type Idempotency struct {
	Enabled          bool          `mapstructure:"enabled"`
	HTTPPaths        []string      `mapstructure:"http_paths"`
	GRPCMethods      []string      `mapstructure:"grpc_methods"`
	ProcessingTTL    time.Duration `mapstructure:"processing_ttl"`
	ResultTTL        time.Duration `mapstructure:"result_ttl"`
	FailureTTL       time.Duration `mapstructure:"failure_ttl"`
	MaxResponseBytes int           `mapstructure:"max_response_bytes"`
}
type EventBus struct {
	Enabled            bool          `mapstructure:"enabled"`
	URLs               []string      `mapstructure:"urls"`
	StreamName         string        `mapstructure:"stream_name"`
	Subjects           []string      `mapstructure:"subjects"`
	Storage            string        `mapstructure:"storage"`
	MaxAge             time.Duration `mapstructure:"max_age"`
	DuplicateWindow    time.Duration `mapstructure:"duplicate_window"`
	ConnectTimeout     time.Duration `mapstructure:"connect_timeout"`
	ReconnectWait      time.Duration `mapstructure:"reconnect_wait"`
	PublishTimeout     time.Duration `mapstructure:"publish_timeout"`
	ConsumerAckWait    time.Duration `mapstructure:"consumer_ack_wait"`
	ConsumerMaxDeliver int           `mapstructure:"consumer_max_deliver"`
	DispatchInterval   time.Duration `mapstructure:"dispatch_interval"`
	DispatchBatchSize  int           `mapstructure:"dispatch_batch_size"`
	DispatchLease      time.Duration `mapstructure:"dispatch_lease"`
	DispatchRetryDelay time.Duration `mapstructure:"dispatch_retry_delay"`
}
type ObjectStorage struct {
	Enabled         bool          `mapstructure:"enabled"`
	Provider        string        `mapstructure:"provider"`
	Bucket          string        `mapstructure:"bucket"`
	Region          string        `mapstructure:"region"`
	Endpoint        string        `mapstructure:"endpoint"`
	AccessKeyID     string        `mapstructure:"access_key_id"`
	AccessKeySecret string        `mapstructure:"access_key_secret"`
	SessionToken    string        `mapstructure:"session_token"`
	UsePathStyle    bool          `mapstructure:"use_path_style"`
	UseCName        bool          `mapstructure:"use_cname"`
	PresignTTL      time.Duration `mapstructure:"presign_ttl"`
}
type Files struct {
	Enabled            bool          `mapstructure:"enabled"`
	MaxSizeBytes       int64         `mapstructure:"max_size_bytes"`
	AllowedTypes       []string      `mapstructure:"allowed_types"`
	DeletionInterval   time.Duration `mapstructure:"deletion_interval"`
	DeletionRetryDelay time.Duration `mapstructure:"deletion_retry_delay"`
	DeletionBatchSize  int           `mapstructure:"deletion_batch_size"`
}
type OperationLog struct {
	Enabled         bool   `mapstructure:"enabled"`
	Subject         string `mapstructure:"subject"`
	Durable         string `mapstructure:"durable"`
	MaxPayloadBytes int    `mapstructure:"max_payload_bytes"`
}
type SecurityLog struct {
	Enabled         bool   `mapstructure:"enabled"`
	Subject         string `mapstructure:"subject"`
	Durable         string `mapstructure:"durable"`
	MaxPayloadBytes int    `mapstructure:"max_payload_bytes"`
	FailClosed      bool   `mapstructure:"fail_closed"`
	HashKey         string `mapstructure:"hash_key"`
}
type DataLifecycle struct {
	Enabled                     bool          `mapstructure:"enabled"`
	Interval                    time.Duration `mapstructure:"interval"`
	PremakeMonths               int           `mapstructure:"premake_months"`
	PurgeBatchSize              int           `mapstructure:"purge_batch_size"`
	OperationLogRetentionMonths int           `mapstructure:"operation_log_retention_months"`
	SecurityLogRetentionMonths  int           `mapstructure:"security_log_retention_months"`
	ArchiveSchema               string        `mapstructure:"archive_schema"`
}
type Outbound struct {
	HTTP map[string]HTTPUpstream `mapstructure:"http"`
	GRPC map[string]GRPCUpstream `mapstructure:"grpc"`
}
type HTTPUpstream struct {
	BaseURL string        `mapstructure:"base_url"`
	Timeout time.Duration `mapstructure:"timeout"`
	Auth    ClientAuth    `mapstructure:"auth"`
	Retry   Retry         `mapstructure:"retry"`
	Breaker Breaker       `mapstructure:"breaker"`
	TLS     ClientTLS     `mapstructure:"tls"`
}
type GRPCUpstream struct {
	Target  string        `mapstructure:"target"`
	Timeout time.Duration `mapstructure:"timeout"`
	Auth    ClientAuth    `mapstructure:"auth"`
	Retry   Retry         `mapstructure:"retry"`
	Breaker Breaker       `mapstructure:"breaker"`
	TLS     ClientTLS     `mapstructure:"tls"`
}
type ClientAuth struct {
	Type  string `mapstructure:"type"`
	Token string `mapstructure:"token"`
}
type Retry struct {
	MaxAttempts    int           `mapstructure:"max_attempts"`
	InitialBackoff time.Duration `mapstructure:"initial_backoff"`
	MaxBackoff     time.Duration `mapstructure:"max_backoff"`
	Methods        []string      `mapstructure:"methods"`
}
type Breaker struct {
	Enabled          bool          `mapstructure:"enabled"`
	FailureThreshold uint32        `mapstructure:"failure_threshold"`
	OpenTimeout      time.Duration `mapstructure:"open_timeout"`
}
type ClientTLS struct {
	Enabled       bool   `mapstructure:"enabled"`
	AllowInsecure bool   `mapstructure:"allow_insecure"`
	ServerName    string `mapstructure:"server_name"`
	CAFile        string `mapstructure:"ca_file"`
	CertFile      string `mapstructure:"cert_file"`
	KeyFile       string `mapstructure:"key_file"`
}

func Load(path string) (Config, error) { return LoadWithProfile(path, "") }

func LoadWithProfile(path, explicitProfile string) (Config, error) {
	v := viper.New()
	if path == "" {
		v.SetConfigName("config")
		v.AddConfigPath(".")
		v.AddConfigPath("./config")
	} else {
		v.SetConfigFile(path)
	}
	v.SetEnvPrefix("APP")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	if err := v.BindEnv("app.env", "APP_ENV", "APP_APP_ENV"); err != nil {
		return Config{}, fmt.Errorf("bind environment profile: %w", err)
	}
	if err := v.BindEnv("outbound.grpc.authorization.target", "APP_OUTBOUND_GRPC_AUTHORIZATION_TARGET"); err != nil {
		return Config{}, fmt.Errorf("bind authorization target: %w", err)
	}
	setDefaults(v)
	readErr := v.ReadInConfig()
	if readErr != nil {
		var notFound viper.ConfigFileNotFoundError
		if path != "" || !errors.As(readErr, &notFound) {
			return Config{}, fmt.Errorf("read config: %w", readErr)
		}
	}
	profile := strings.ToLower(strings.TrimSpace(explicitProfile))
	if profile == "" {
		profile = strings.ToLower(strings.TrimSpace(v.GetString("app.env")))
	}
	if !validProfile.MatchString(profile) {
		return Config{}, fmt.Errorf("invalid environment profile %q", profile)
	}
	basePath := ""
	loadedFiles := make([]string, 0, 2)
	if readErr == nil {
		basePath = v.ConfigFileUsed()
		loadedFiles = append(loadedFiles, basePath)
	}
	profilePath := profileConfigPath(basePath, profile)
	if profilePath != basePath {
		if _, err := os.Stat(profilePath); err == nil {
			v.SetConfigFile(profilePath)
			if err := v.MergeInConfig(); err != nil {
				return Config{}, fmt.Errorf("merge profile config %q: %w", profilePath, err)
			}
			loadedFiles = append(loadedFiles, profilePath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("inspect profile config %q: %w", profilePath, err)
		}
	}
	v.Set("app.env", profile)
	var cfg Config
	if err := v.Unmarshal(
		&cfg,
		viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			stringToStringSliceHook(),
		)),
	); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	cfg.Migration.Schema = cfg.Database.Schema
	cfg.Migration.DatabaseName = cfg.Database.Name
	cfg.Log.Level = strings.ToLower(strings.TrimSpace(cfg.Log.Level))
	cfg.Log.Format = strings.ToLower(strings.TrimSpace(cfg.Log.Format))
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	cfg.Runtime = Runtime{ActiveProfile: profile, ConfigFiles: loadedFiles}
	return cfg, nil
}

func stringToStringSliceHook() mapstructure.DecodeHookFuncType {
	stringSliceType := reflect.TypeFor[[]string]()
	return func(from reflect.Type, to reflect.Type, data any) (any, error) {
		if from.Kind() != reflect.String || to != stringSliceType {
			return data, nil
		}
		raw := strings.TrimSpace(data.(string))
		if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
			raw = strings.TrimSpace(raw[1 : len(raw)-1])
		}
		if raw == "" {
			return []string{}, nil
		}
		values := strings.Split(raw, ",")
		for index := range values {
			values[index] = strings.Trim(strings.TrimSpace(values[index]), `"'`)
		}
		return values, nil
	}
}

var validProfile = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
var validMigrationTable = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
var validRedisPrefix = regexp.MustCompile(`^[a-zA-Z0-9._:-]{1,127}:$`)

func profileConfigPath(path, profile string) string {
	if path == "" {
		return "config-" + profile + ".yaml"
	}
	extension := filepath.Ext(path)
	base := strings.TrimSuffix(path, extension)
	return base + "-" + profile + extension
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("app.name", "go-api-template")
	v.SetDefault("app.env", "development")
	v.SetDefault("app.shutdown_timeout", "10s")
	v.SetDefault("http.address", "127.0.0.1:8080")
	v.SetDefault("http.read_timeout", "10s")
	v.SetDefault("http.write_timeout", "15s")
	v.SetDefault("http.idle_timeout", "60s")
	v.SetDefault("http.request_timeout", "10s")
	v.SetDefault("http.max_body_bytes", 16<<20)
	v.SetDefault("http.trusted_proxies", []string{})
	v.SetDefault("http.cors.enabled", false)
	v.SetDefault("http.cors.allowed_origins", []string{})
	v.SetDefault("http.cors.allowed_headers", []string{"Authorization", "Content-Type", "X-Request-ID", "Idempotency-Key"})
	v.SetDefault("http.cors.exposed_headers", []string{"X-Request-ID", "X-RateLimit-Limit", "X-RateLimit-Remaining", "Retry-After"})
	v.SetDefault("http.cors.max_age", "12h")
	v.SetDefault("grpc.enabled", true)
	v.SetDefault("grpc.address", "127.0.0.1:9090")
	v.SetDefault("grpc.reflection_enabled", true)
	v.SetDefault("grpc.max_receive_bytes", 16<<20)
	v.SetDefault("grpc.tls.enabled", false)
	v.SetDefault("grpc.tls.cert_file", "")
	v.SetDefault("grpc.tls.key_file", "")
	v.SetDefault("grpc.tls.client_ca_file", "")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("log.file", "logs/app.log")
	v.SetDefault("log.max_size_mb", 100)
	v.SetDefault("log.max_backups", 10)
	v.SetDefault("log.max_age_days", 30)
	v.SetDefault("log.compress", true)
	v.SetDefault("database.ping_timeout", "5s")
	v.SetDefault("database.max_open_conns", 25)
	v.SetDefault("database.max_idle_conns", 10)
	v.SetDefault("database.conn_max_lifetime", "5m")
	v.SetDefault("database.conn_max_idle_time", "1m")
	v.SetDefault("database.enabled", false)
	v.SetDefault("database.name", "go_api_template_db")
	v.SetDefault("database.schema", "go_api_template")
	v.SetDefault("database.type", "postgres")
	v.SetDefault("database.dsn", "")
	v.SetDefault("redis.address", "127.0.0.1:6379")
	v.SetDefault("redis.dial_timeout", "5s")
	v.SetDefault("redis.read_timeout", "3s")
	v.SetDefault("redis.write_timeout", "3s")
	v.SetDefault("redis.enabled", false)
	v.SetDefault("redis.key_prefix", "")
	v.SetDefault("redis.username", "")
	v.SetDefault("redis.password", "")
	v.SetDefault("redis.db", 0)
	v.SetDefault("health.database_timeout", "2s")
	v.SetDefault("health.redis_timeout", "2s")
	v.SetDefault("rate_limit.enabled", false)
	v.SetDefault("rate_limit.fail_open", false)
	setRateLimitDefaults(v, "rate_limit.ip", 120, 30)
	setRateLimitDefaults(v, "rate_limit.api", 10000, 1000)
	setRateLimitDefaults(v, "rate_limit.user", 600, 100)
	setRateLimitDefaults(v, "rate_limit.login", 10, 3)
	v.SetDefault("observability.metrics_enabled", true)
	v.SetDefault("observability.tracing_enabled", false)
	v.SetDefault("observability.tracing_endpoint", "http://127.0.0.1:4318")
	v.SetDefault("observability.tracing_sample_ratio", 0.1)
	v.SetDefault("observability.pprof_enabled", false)
	v.SetDefault("observability.pprof_token", "")
	v.SetDefault("swagger.enabled", true)
	v.SetDefault("swagger.require_auth", false)
	v.SetDefault("jwt.issuer", "go-api-template")
	v.SetDefault("jwt.audience", "go-api-template")
	v.SetDefault("jwt.algorithm", "RS256")
	v.SetDefault("jwt.key_id", "")
	v.SetDefault("jwt.private_key", "")
	v.SetDefault("jwt.private_key_file", "")
	v.SetDefault("jwt.public_keys", []JWTVerificationKey{})
	v.SetDefault("jwt.ttl", "2h")
	v.SetDefault("auth.jwks_url", "")
	v.SetDefault("auth.issuer", "identity-service")
	v.SetDefault("auth.audience", "go-api-template")
	v.SetDefault("auth.psk.enabled", false)
	v.SetDefault("auth.psk.key", "")
	v.SetDefault("authorization.enabled", false)
	v.SetDefault("authentication.refresh_ttl", "720h")
	v.SetDefault("authentication.max_failed_attempts", 5)
	v.SetDefault("authentication.lock_duration", "15m")
	v.SetDefault("cron.enabled", true)
	v.SetDefault("cron.timezone", "Asia/Shanghai")
	v.SetDefault("cron.sample_spec", "0 */5 * * * *")
	v.SetDefault("migration.path", "migrations/postgres")
	v.SetDefault("migration.database_url", "")
	v.SetDefault("migration.auto_up", false)
	v.SetDefault("migration.create_schema", false)
	v.SetDefault("migration.table", "go_api_template_schema_migrations")
	v.SetDefault("authorization.policy_refresh_interval", 30*time.Second)
	v.SetDefault("user.cache_ttl", "5m")
	v.SetDefault("user.lock_ttl", "10s")
	v.SetDefault("user.lock_retry_delay", "100ms")
	v.SetDefault("tenant.cache_ttl", "5m")
	v.SetDefault("menu.cache_ttl", "5m")
	v.SetDefault("menu.max_nodes", 10000)
	v.SetDefault("platform_config.cache_ttl", "5m")
	v.SetDefault("idempotency.enabled", false)
	v.SetDefault("idempotency.processing_ttl", "30s")
	v.SetDefault("idempotency.result_ttl", "24h")
	v.SetDefault("idempotency.failure_ttl", "5m")
	v.SetDefault("idempotency.max_response_bytes", 1048576)
	v.SetDefault("event_bus.enabled", false)
	v.SetDefault("event_bus.urls", []string{"nats://127.0.0.1:4222"})
	v.SetDefault("event_bus.stream_name", "PLATFORM_EVENTS")
	v.SetDefault("event_bus.subjects", []string{"platform.>"})
	v.SetDefault("event_bus.storage", "file")
	v.SetDefault("event_bus.max_age", "168h")
	v.SetDefault("event_bus.duplicate_window", "10m")
	v.SetDefault("event_bus.connect_timeout", "5s")
	v.SetDefault("event_bus.reconnect_wait", "1s")
	v.SetDefault("event_bus.publish_timeout", "5s")
	v.SetDefault("event_bus.consumer_ack_wait", "30s")
	v.SetDefault("event_bus.consumer_max_deliver", 10)
	v.SetDefault("event_bus.dispatch_interval", "1s")
	v.SetDefault("event_bus.dispatch_batch_size", 100)
	v.SetDefault("event_bus.dispatch_lease", "30s")
	v.SetDefault("event_bus.dispatch_retry_delay", "2s")
	v.SetDefault("object_storage.enabled", false)
	v.SetDefault("object_storage.provider", "s3")
	v.SetDefault("object_storage.bucket", "")
	v.SetDefault("object_storage.region", "")
	v.SetDefault("object_storage.endpoint", "")
	v.SetDefault("object_storage.access_key_id", "")
	v.SetDefault("object_storage.access_key_secret", "")
	v.SetDefault("object_storage.session_token", "")
	v.SetDefault("object_storage.use_path_style", false)
	v.SetDefault("object_storage.use_cname", false)
	v.SetDefault("object_storage.presign_ttl", "15m")
	v.SetDefault("files.enabled", false)
	v.SetDefault("files.max_size_bytes", 10<<20)
	v.SetDefault("files.allowed_types", []string{})
	v.SetDefault("files.deletion_interval", "30s")
	v.SetDefault("files.deletion_retry_delay", "1m")
	v.SetDefault("files.deletion_batch_size", 100)
	v.SetDefault("operation_log.enabled", false)
	v.SetDefault("operation_log.subject", "platform.operation-log.v1")
	v.SetDefault("operation_log.durable", "go-api-template-operation-log")
	v.SetDefault("operation_log.max_payload_bytes", 8192)
	v.SetDefault("security_log.enabled", false)
	v.SetDefault("security_log.subject", "platform.security-log.v1")
	v.SetDefault("security_log.durable", "go-api-template-security-log")
	v.SetDefault("security_log.max_payload_bytes", 4096)
	v.SetDefault("security_log.fail_closed", true)
	v.SetDefault("security_log.hash_key", "")
	v.SetDefault("data_lifecycle.enabled", false)
	v.SetDefault("data_lifecycle.interval", "1h")
	v.SetDefault("data_lifecycle.premake_months", 6)
	v.SetDefault("data_lifecycle.purge_batch_size", 1000)
	v.SetDefault("data_lifecycle.operation_log_retention_months", 12)
	v.SetDefault("data_lifecycle.security_log_retention_months", 24)
	v.SetDefault("data_lifecycle.archive_schema", "")
	v.SetDefault("outbound.http", map[string]any{})
	v.SetDefault("outbound.grpc", map[string]any{})
}

func (c Config) Validate() error {
	if !validMigrationTable.MatchString(c.Database.Name) {
		return errors.New("database.name must contain lowercase letters, digits, or underscores and be at most 63 characters")
	}
	if c.Database.Schema != "" && !validMigrationTable.MatchString(c.Database.Schema) {
		return errors.New("database.schema must contain lowercase letters, digits, or underscores and be at most 63 characters")
	}
	if c.HTTP.Address == "" {
		return errors.New("http.address is required")
	}
	if c.HTTP.ReadTimeout <= 0 || c.HTTP.WriteTimeout <= 0 || c.HTTP.IdleTimeout <= 0 || c.HTTP.RequestTimeout <= 0 || c.HTTP.MaxBodyBytes <= 0 || c.HTTP.MaxBodyBytes > 1<<30 {
		return errors.New("http requires positive timeouts and max_body_bytes no greater than 1 GiB")
	}
	if c.Log.Level != "debug" && c.Log.Level != "info" && c.Log.Level != "warn" && c.Log.Level != "error" {
		return errors.New("log.level must be debug, info, warn, or error")
	}
	if c.Log.Format != "json" && c.Log.Format != "text" {
		return errors.New("log.format must be json or text")
	}
	if strings.TrimSpace(c.Log.File) == "" || c.Log.MaxSizeMB <= 0 || c.Log.MaxSizeMB > 10240 || c.Log.MaxBackups <= 0 || c.Log.MaxBackups > 1000 || c.Log.MaxAgeDays <= 0 || c.Log.MaxAgeDays > 3650 {
		return errors.New("log requires a file and bounded positive rotation settings")
	}
	if c.GRPC.Enabled && (c.GRPC.Address == "" || c.GRPC.MaxReceiveBytes <= 0) {
		return errors.New("enabled grpc requires address and positive max_receive_bytes")
	}
	if c.GRPC.TLS.Enabled && (c.GRPC.TLS.CertFile == "" || c.GRPC.TLS.KeyFile == "") {
		return errors.New("grpc tls requires cert_file and key_file")
	}
	if c.App.Env == "production" && c.GRPC.Enabled && !c.GRPC.TLS.Enabled {
		return errors.New("grpc tls must be enabled in production")
	}
	if c.App.Env == "production" && c.GRPC.ReflectionEnabled {
		return errors.New("grpc reflection must be disabled in production")
	}
	if c.Database.Enabled && (c.Database.DSN == "" || !isDBType(c.Database.Type)) {
		return errors.New("enabled database requires dsn and type mysql, postgres, or kingbase")
	}
	if c.Database.Enabled && (c.Database.MaxOpenConns <= 0 || c.Database.MaxIdleConns < 0 || c.Database.MaxIdleConns > c.Database.MaxOpenConns || c.Database.ConnMaxLifetime <= 0 || c.Database.ConnMaxIdleTime <= 0 || c.Database.PingTimeout <= 0) {
		return errors.New("enabled database requires bounded pool sizes and positive connection and ping timeouts")
	}
	if c.Migration.AutoUp && (!c.Database.Enabled || c.Migration.Path == "" || c.Migration.DatabaseURL == "" || !validMigrationTable.MatchString(c.Migration.Table)) {
		return errors.New("migration.auto_up requires enabled database, path, database_url, and a valid service-specific table")
	}
	if c.Redis.Enabled && (c.Redis.Address == "" || c.Redis.DialTimeout <= 0 || c.Redis.DialTimeout > time.Minute || c.Redis.ReadTimeout <= 0 || c.Redis.ReadTimeout > time.Minute || c.Redis.WriteTimeout <= 0 || c.Redis.WriteTimeout > time.Minute) {
		return errors.New("enabled redis requires an address and positive timeouts no greater than one minute")
	}
	if c.Redis.KeyPrefix != "" && !validRedisPrefix.MatchString(c.Redis.KeyPrefix) {
		return errors.New("redis.key_prefix must be a bounded namespace ending in ':'")
	}
	if c.Health.DatabaseTimeout <= 0 || c.Health.RedisTimeout <= 0 {
		return errors.New("http and health timeouts must be positive")
	}
	if c.RateLimit.Enabled && !c.Redis.Enabled {
		return errors.New("rate_limit requires redis.enabled")
	}
	if c.RateLimit.Enabled {
		for name, rule := range map[string]RateLimitRule{"ip": c.RateLimit.IP, "api": c.RateLimit.API, "user": c.RateLimit.User, "login": c.RateLimit.Login} {
			if rule.Rate <= 0 || rule.Rate > 1_000_000 || rule.Burst <= 0 || rule.Burst > 1_000_000 || rule.Period < time.Second || rule.Period > 24*time.Hour {
				return fmt.Errorf("rate_limit.%s values must be bounded and positive", name)
			}
		}
	}
	if c.HTTP.CORS.Enabled && len(c.HTTP.CORS.AllowedOrigins) == 0 {
		return errors.New("cors.allowed_origins is required when cors is enabled")
	}
	if c.Observability.TracingSampleRatio < 0 || c.Observability.TracingSampleRatio > 1 {
		return errors.New("observability.tracing_sample_ratio must be between 0 and 1")
	}
	if c.Observability.TracingEnabled && c.Observability.TracingEndpoint == "" {
		return errors.New("observability.tracing_endpoint is required")
	}
	if c.Observability.PprofEnabled && len(c.Observability.PprofToken) < 32 {
		return errors.New("observability.pprof_token must contain at least 32 bytes")
	}
	if c.App.Env == "production" && c.Swagger.Enabled && !c.Swagger.RequireAuth {
		return errors.New("swagger.require_auth must be enabled in production")
	}
	if c.App.Env == "production" && (c.Auth.JWKSURL == "" || c.Auth.Issuer == "" || c.Auth.Audience == "") {
		return errors.New("production authentication requires identity JWKS URL, issuer, and service audience")
	}
	if c.App.Env == "production" && !c.Authorization.Enabled {
		return errors.New("authorization must be enabled in production")
	}
	if c.Authorization.Enabled {
		if _, ok := c.Outbound.GRPC["authorization"]; !ok {
			return errors.New("enabled authorization requires outbound.grpc.authorization")
		}
		if c.Authorization.PolicyRefreshInterval < 100*time.Millisecond || c.Authorization.PolicyRefreshInterval > 10*time.Minute {
			return errors.New("authorization.policy_refresh_interval must be between 100ms and 10m")
		}
	}
	hasSigningKey := c.JWT.PrivateKey != "" || c.JWT.PrivateKeyFile != ""
	if (c.JWT.KeyID != "") != hasSigningKey {
		return errors.New("jwt.key_id and an asymmetric private key must be configured together")
	}
	if c.App.Env == "production" && !hasSigningKey {
		return errors.New("production authentication requires an asymmetric JWT signing key")
	}
	if c.JWT.Algorithm != "" && c.JWT.Algorithm != "RS256" && c.JWT.Algorithm != "ES256" {
		return errors.New("jwt.algorithm must be RS256 or ES256")
	}
	if c.Auth.PSK.Enabled && len(c.Auth.PSK.Key) < 32 {
		return errors.New("enabled auth.psk requires a key of at least 32 bytes")
	}
	const maxCacheTTL = 24 * time.Hour
	if c.User.CacheTTL <= 0 || c.User.CacheTTL > maxCacheTTL || c.User.LockTTL <= 0 || c.User.LockRetryDelay <= 0 {
		return errors.New("user cache duration must be positive and no greater than 24h; lock durations must be positive")
	}
	if c.Tenant.CacheTTL <= 0 || c.Tenant.CacheTTL > maxCacheTTL {
		return errors.New("tenant cache duration must be positive and no greater than 24h")
	}
	if c.Menu.CacheTTL <= 0 || c.Menu.CacheTTL > maxCacheTTL || c.Menu.MaxNodes <= 0 || c.Menu.MaxNodes > 100000 {
		return errors.New("menu cache duration must be positive and no greater than 24h; max_nodes must be valid")
	}
	if c.PlatformConfig.CacheTTL <= 0 || c.PlatformConfig.CacheTTL > maxCacheTTL {
		return errors.New("platform config cache duration must be positive and no greater than 24h")
	}
	if c.Authentication.RefreshTTL <= 0 || c.Authentication.MaxFailedAttempts <= 0 || c.Authentication.LockDuration <= 0 {
		return errors.New("authentication refresh, failure, and lock settings must be positive")
	}
	if c.Idempotency.Enabled && (!c.Redis.Enabled || (len(c.Idempotency.HTTPPaths) == 0 && len(c.Idempotency.GRPCMethods) == 0) || c.Idempotency.ProcessingTTL <= 0 || c.Idempotency.ResultTTL <= 0 || c.Idempotency.FailureTTL <= 0 || c.Idempotency.MaxResponseBytes <= 0 || c.Idempotency.MaxResponseBytes > 16<<20) {
		return errors.New("enabled idempotency requires redis, at least one route pattern, positive TTL values, and max_response_bytes no greater than 16 MiB")
	}
	for _, pattern := range c.Idempotency.HTTPPaths {
		if !strings.HasPrefix(pattern, "/api/") {
			return fmt.Errorf("idempotency.http_paths contains path outside /api %q", pattern)
		}
		if _, err := path.Match(pattern, "/validation/target"); err != nil {
			return fmt.Errorf("idempotency.http_paths contains invalid pattern %q: %w", pattern, err)
		}
	}
	for _, pattern := range c.Idempotency.GRPCMethods {
		if !strings.HasPrefix(pattern, "/") || strings.Count(pattern, "/") != 2 {
			return fmt.Errorf("idempotency.grpc_methods contains invalid method pattern %q", pattern)
		}
		if _, err := path.Match(pattern, "/validation/target"); err != nil {
			return fmt.Errorf("idempotency.grpc_methods contains invalid pattern %q: %w", pattern, err)
		}
	}
	if c.EventBus.Enabled && (len(c.EventBus.URLs) == 0 || c.EventBus.StreamName == "" || len(c.EventBus.Subjects) != 1 || c.EventBus.Subjects[0] != "platform.>" || (c.EventBus.Storage != "file" && c.EventBus.Storage != "memory") || c.EventBus.MaxAge <= 0 || c.EventBus.DuplicateWindow <= 0 || c.EventBus.ConnectTimeout <= 0 || c.EventBus.ReconnectWait <= 0 || c.EventBus.PublishTimeout <= 0 || c.EventBus.ConsumerAckWait <= 0 || c.EventBus.ConsumerMaxDeliver <= 0 || c.EventBus.ConsumerMaxDeliver > 100 || c.EventBus.DispatchInterval < 10*time.Millisecond || c.EventBus.DispatchInterval > time.Minute || c.EventBus.DispatchBatchSize <= 0 || c.EventBus.DispatchBatchSize > 1000 || c.EventBus.DispatchLease <= c.EventBus.PublishTimeout || c.EventBus.DispatchLease > 10*time.Minute || c.EventBus.DispatchRetryDelay <= 0 || c.EventBus.DispatchRetryDelay > time.Hour) {
		return errors.New("enabled event_bus requires URLs, stream, canonical platform.> subjects, valid storage, positive timeouts, delivery, and dispatch settings")
	}
	if c.ObjectStorage.Enabled && ((c.ObjectStorage.Provider != "s3" && c.ObjectStorage.Provider != "oss") || c.ObjectStorage.Bucket == "" || c.ObjectStorage.Region == "" || c.ObjectStorage.PresignTTL <= 0 || c.ObjectStorage.PresignTTL > 24*time.Hour) {
		return errors.New("enabled object_storage requires provider s3 or oss, bucket, region, and presign_ttl no greater than 24h")
	}
	if (c.ObjectStorage.AccessKeyID == "") != (c.ObjectStorage.AccessKeySecret == "") {
		return errors.New("object_storage access_key_id and access_key_secret must be configured together")
	}
	if c.Files.Enabled && (!c.Database.Enabled || !c.ObjectStorage.Enabled || c.Files.MaxSizeBytes <= 0 || c.Files.MaxSizeBytes > c.HTTP.MaxBodyBytes) {
		return errors.New("enabled files requires database, object_storage, and positive max_size_bytes not exceeding http.max_body_bytes")
	}
	if c.Files.Enabled && (c.Files.DeletionInterval <= 0 || c.Files.DeletionRetryDelay <= 0 || c.Files.DeletionBatchSize <= 0 || c.Files.DeletionBatchSize > 1000) {
		return errors.New("enabled files requires valid deletion retry settings")
	}
	if c.OperationLog.Enabled && (!c.Database.Enabled || !c.EventBus.Enabled || c.OperationLog.Subject == "" || c.OperationLog.Durable == "" || c.OperationLog.MaxPayloadBytes <= 0) {
		return errors.New("enabled operation_log requires database, event_bus, subject, durable, and positive payload limit")
	}
	if c.SecurityLog.Enabled && (!c.Database.Enabled || !c.EventBus.Enabled || c.SecurityLog.Subject == "" || c.SecurityLog.Durable == "" || c.SecurityLog.MaxPayloadBytes <= 0 || len(c.SecurityLog.HashKey) < 32) {
		return errors.New("enabled security_log requires database, event_bus, subject, durable, positive payload limit, and a hash_key of at least 32 bytes")
	}
	if c.App.Env == "production" && (!c.EventBus.Enabled || !c.OperationLog.Enabled || !c.SecurityLog.Enabled) {
		return errors.New("production authentication requires event_bus, operation_log, and security_log")
	}
	if c.DataLifecycle.Enabled {
		if !c.Database.Enabled {
			return errors.New("data_lifecycle requires an enabled database")
		}
		if c.DataLifecycle.Interval <= 0 || c.DataLifecycle.PremakeMonths < 1 || c.DataLifecycle.PremakeMonths > 24 || c.DataLifecycle.PurgeBatchSize < 1 || c.DataLifecycle.PurgeBatchSize > 10000 || c.DataLifecycle.OperationLogRetentionMonths < 1 || c.DataLifecycle.SecurityLogRetentionMonths < 1 {
			return errors.New("data_lifecycle requires a positive interval and retention months, premake_months between 1 and 24, and purge_batch_size between 1 and 10000")
		}
		if c.DataLifecycle.ArchiveSchema != "" && !validMigrationTable.MatchString(c.DataLifecycle.ArchiveSchema) {
			return errors.New("data_lifecycle.archive_schema must contain lowercase letters, digits, or underscores and be at most 63 characters")
		}
		if c.Database.Type == "mysql" && c.DataLifecycle.ArchiveSchema != "" {
			return errors.New("mysql data_lifecycle requires an empty archive_schema and uses bounded purge batches")
		}
	}
	for name, upstream := range c.Outbound.HTTP {
		if upstream.BaseURL == "" || upstream.Timeout <= 0 {
			return fmt.Errorf("outbound.http.%s requires base_url and positive timeout", name)
		}
		if err := validateClientPolicy(name, upstream.Auth, upstream.Retry, upstream.Breaker, upstream.TLS, c.App.Env == "production"); err != nil {
			return err
		}
	}
	for name, upstream := range c.Outbound.GRPC {
		if upstream.Target == "" || upstream.Timeout <= 0 {
			return fmt.Errorf("outbound.grpc.%s requires target and positive timeout", name)
		}
		if err := validateClientPolicy(name, upstream.Auth, upstream.Retry, upstream.Breaker, upstream.TLS, c.App.Env == "production"); err != nil {
			return err
		}
	}
	return nil
}

func validateClientPolicy(name string, auth ClientAuth, retry Retry, breaker Breaker, tls ClientTLS, production bool) error {
	if auth.Type != "" && auth.Type != "bearer" && auth.Type != "psk" {
		return fmt.Errorf("outbound %s auth.type must be bearer or psk", name)
	}
	if auth.Type != "" && auth.Token == "" {
		return fmt.Errorf("outbound %s auth.token is required", name)
	}
	if auth.Type != "" && !tls.Enabled && !tls.AllowInsecure {
		return fmt.Errorf("outbound %s credentials require TLS or explicit allow_insecure", name)
	}
	if tls.Enabled && tls.AllowInsecure {
		return fmt.Errorf("outbound %s TLS and allow_insecure are mutually exclusive", name)
	}
	if production && auth.Type != "" && tls.AllowInsecure {
		return fmt.Errorf("production outbound %s credentials require TLS", name)
	}
	if retry.MaxAttempts < 1 || retry.MaxAttempts > 5 || retry.InitialBackoff <= 0 || retry.MaxBackoff < retry.InitialBackoff {
		return fmt.Errorf("outbound %s retry policy is invalid", name)
	}
	for _, pattern := range retry.Methods {
		if !strings.HasPrefix(pattern, "/") || strings.Count(pattern, "/") != 2 {
			return fmt.Errorf("outbound %s retry method pattern is invalid", name)
		}
		if _, err := path.Match(pattern, "/validation/target"); err != nil {
			return fmt.Errorf("outbound %s retry method pattern is invalid: %w", name, err)
		}
	}
	if breaker.Enabled && (breaker.FailureThreshold == 0 || breaker.OpenTimeout <= 0) {
		return fmt.Errorf("outbound %s breaker policy is invalid", name)
	}
	if tls.Enabled && (tls.CertFile == "") != (tls.KeyFile == "") {
		return fmt.Errorf("outbound %s TLS certificate and key must be configured together", name)
	}
	return nil
}

func setRateLimitDefaults(v *viper.Viper, key string, rate, burst int) {
	v.SetDefault(key+".rate", rate)
	v.SetDefault(key+".burst", burst)
	v.SetDefault(key+".period", "1m")
}

func isDBType(value string) bool {
	return value == "mysql" || value == "postgres" || value == "kingbase"
}
