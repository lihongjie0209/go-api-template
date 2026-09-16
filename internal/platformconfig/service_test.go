package platformconfig

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/lihongjie0209/microservice-platform-go/stableid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type actorResolverStub struct {
	calls int
}

func (s *actorResolverStub) ResolveUserIDs(_ context.Context, _ []string) (map[string]string, error) {
	s.calls++
	return map[string]string{"user-1": "Alice"}, nil
}

func TestPlatformConfigPresentationBatchesActorsAndUsesPlatformTimezone(t *testing.T) {
	t.Parallel()
	resolver := &actorResolverStub{}
	service := &Service{actors: resolver}
	instant := time.Date(2026, time.September, 16, 1, 2, 3, 0, time.UTC)
	records := []Record{{CreatedBy: "user-1", UpdatedBy: "system-1", CreatedAt: instant, UpdatedAt: instant}, {CreatedBy: "user-1", UpdatedBy: "user-1", CreatedAt: instant, UpdatedAt: instant}}

	require.NoError(t, service.present(t.Context(), records))
	require.Equal(t, 1, resolver.calls)
	require.Equal(t, "Alice", records[0].CreatedByName)
	require.Equal(t, "system-1", records[0].UpdatedByName)
	require.Equal(t, "2026-09-16T09:02:03+08:00", records[0].CreatedAt.Format(time.RFC3339))
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name, key, status string
		value             json.RawMessage
		wantErr           bool
	}{{"valid", "ui.theme", "active", json.RawMessage(`{"primary":"blue"}`), false}, {"invalid key", "UI KEY", "active", json.RawMessage(`{}`), true}, {"invalid json", "ui.theme", "active", json.RawMessage(`{`), true}, {"invalid status", "ui.theme", "unknown", json.RawMessage(`{}`), true}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validate(test.key, "name", "ui", "", test.status, test.value)
			if test.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateRejectsSecretLikeConfiguration(t *testing.T) {
	require.ErrorIs(t, validate("database.password", "Database", "system", "", "active", json.RawMessage(`"value"`)), ErrInvalid)
	require.ErrorIs(t, validate("ui.bootstrap", "Bootstrap", "ui", "", "active", json.RawMessage(`{"nested":{"access_token":"value"}}`)), ErrInvalid)
	require.NoError(t, validate("ui.bootstrap", "Bootstrap", "ui", "", "active", json.RawMessage(`{"nested":{"color":"blue"}}`)))
}

func TestPlatformConfigStableIDGoldenMapping(t *testing.T) {
	t.Parallel()
	generator, err := stableid.New(platformConfigNamespace)
	require.NoError(t, err)
	id, err := generator.String("platform-config:ui.theme")
	require.NoError(t, err)
	require.Equal(t, "20f40b91-76f1-5d16-bade-fc464610cbde", id)
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
		{Request: pagination.Request{Page: 1}, IDs: []string{" "}},
		{Request: pagination.Request{Page: 1}, Keyword: string(make([]byte, maxConfigKeywordLength+1))},
		{Request: pagination.Request{Page: 1}, Statuses: []string{"unknown"}},
		{Request: pagination.Request{Page: 1}, ValueTypes: []string{"secret"}},
		{Request: pagination.Request{Page: 1}, CreatedAtFrom: &from, CreatedAtTo: &to},
	} {
		_, err := service.Page(ctx, input)
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestListPublicRejectsUnboundedResult(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service := &Service{db: sqlx.NewDb(db, "sqlmock")}
	rows := sqlmock.NewRows([]string{"config_key", "name", "category", "value_type", "value"})
	for index := 0; index <= maxPublicConfigs; index++ {
		rows.AddRow("ui.item", "Item", "ui", "boolean", []byte(`true`))
	}
	mock.ExpectQuery(`SELECT config_key,name,category,value_type,value.*LIMIT 1001`).WillReturnRows(rows)

	_, err = service.ListPublic(t.Context(), "")
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, mock.ExpectationsWereMet())
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

func TestMutationRollsBackWhenTransactionalAuditFails(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	recorder := &failingOperationRecorder{err: errors.New("outbox unavailable")}
	service := &Service{
		tx: database.NewTransactor(sqlxDB), operations: recorder, security: configSecurityRecorder{},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mock.ExpectBegin()
	mock.ExpectRollback()

	err = service.mutate(platformprincipal.SystemContext(t.Context(), "actor-1"), "platform.config.update", "config-1", "ui.theme", nil, func(context.Context, *sqlx.Tx) error { return nil })
	require.ErrorContains(t, err, "outbox unavailable")
	require.Equal(t, 1, recorder.transactionalCalls)
	require.Equal(t, 1, recorder.failureCalls)
	require.NoError(t, mock.ExpectationsWereMet())
}

type failingOperationRecorder struct {
	err                error
	transactionalCalls int
	failureCalls       int
}

func (*failingOperationRecorder) Enabled() bool { return true }
func (r *failingOperationRecorder) Record(_ context.Context, entry operationlog.Entry) error {
	if !entry.Succeeded {
		r.failureCalls++
	}
	return nil
}
func (r *failingOperationRecorder) RecordTx(context.Context, *sqlx.Tx, operationlog.Entry) error {
	r.transactionalCalls++
	return r.err
}

type configSecurityRecorder struct{}

func (configSecurityRecorder) Enabled() bool                                   { return true }
func (configSecurityRecorder) FailClosed() bool                                { return true }
func (configSecurityRecorder) Record(context.Context, securitylog.Entry) error { return nil }
func (configSecurityRecorder) RecordTx(context.Context, *sqlx.Tx, securitylog.Entry) error {
	return nil
}
