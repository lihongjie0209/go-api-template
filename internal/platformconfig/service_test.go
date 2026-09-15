package platformconfig

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name, key, status string
		value             json.RawMessage
		wantErr           bool
	}{{"valid", "ui.theme", "active", json.RawMessage(`{"primary":"blue"}`), false}, {"invalid key", "UI KEY", "active", json.RawMessage(`{}`), true}, {"invalid json", "ui.theme", "active", json.RawMessage(`{`), true}, {"invalid status", "ui.theme", "unknown", json.RawMessage(`{}`), true}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validate(test.key, "name", test.status, test.value)
			if test.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateRejectsSecretLikeConfiguration(t *testing.T) {
	require.ErrorIs(t, validate("database.password", "Database", "active", json.RawMessage(`"value"`)), ErrInvalid)
	require.ErrorIs(t, validate("ui.bootstrap", "Bootstrap", "active", json.RawMessage(`{"nested":{"access_token":"value"}}`)), ErrInvalid)
	require.NoError(t, validate("ui.bootstrap", "Bootstrap", "active", json.RawMessage(`{"nested":{"color":"blue"}}`)))
}

func TestValueType(t *testing.T) {
	tests := map[string]string{`"text"`: "string", `42`: "number", `true`: "boolean", `{}`: "object", `[]`: "array", `null`: "null"}
	for raw, expected := range tests {
		require.Equal(t, expected, valueType(json.RawMessage(raw)))
	}
}

func TestPageRejectsInvalidFiltersBeforeDatabase(t *testing.T) {
	service := &Service{}
	ctx := platformprincipal.SystemContext(t.Context(), "admin")
	to := time.Now()
	from := to.Add(time.Hour)
	for _, input := range []PageInput{
		{Request: pagination.Request{Page: 1}, IDs: make([]string, 201)},
		{Request: pagination.Request{Page: 1}, Statuses: []string{"unknown"}},
		{Request: pagination.Request{Page: 1}, ValueTypes: []string{"secret"}},
		{Request: pagination.Request{Page: 1}, CreatedAtFrom: &from, CreatedAtTo: &to},
	} {
		_, err := service.Page(ctx, input)
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestGetPublicUsesTypedDistributedCache(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	service := &Service{db: sqlx.NewDb(db, "sqlmock"), cache: cache.NewRedisStore(client), cfg: config.Config{PlatformConfig: config.PlatformConfig{CacheTTL: time.Minute}}}
	mock.ExpectQuery(`SELECT config_key,name,category,value_type,value FROM platform_configs`).
		WithArgs("ui.theme").
		WillReturnRows(sqlmock.NewRows([]string{"config_key", "name", "category", "value_type", "value"}).AddRow("ui.theme", "Theme", "ui", "object", []byte(`{"color":"blue"}`)))

	first, err := service.GetPublic(context.Background(), "UI.THEME")
	require.NoError(t, err)
	second, err := service.GetPublic(context.Background(), "ui.theme")
	require.NoError(t, err)
	require.Equal(t, "object", first.ValueType)
	require.Equal(t, first, second)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConfigLogRequestNeverContainsValue(t *testing.T) {
	payload, err := json.Marshal(configLogRequest("ui.theme", "Theme", "ui", true, "active", 2))
	require.NoError(t, err)
	require.NotContains(t, string(payload), `"value":`)
	require.Contains(t, string(payload), `"value_redacted":true`)
}
