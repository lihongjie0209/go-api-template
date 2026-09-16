package platformconfig

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/lihongjie0209/microservice-platform-go/stableid"
)

const platformConfigNamespace = "aa4b92d2-9002-4523-b390-27b57ffe65f2"

const (
	maxConfigIDLength          = 128
	maxConfigNameLength        = 256
	maxConfigCategoryLength    = 128
	maxConfigDescriptionLength = 4096
	maxConfigKeywordLength     = 256
	maxConfigValueBytes        = 1 << 20
	maxPublicConfigs           = 1000
)

var (
	ErrInvalid          = errors.New("invalid platform config")
	ErrNotFound         = errors.New("platform config not found")
	ErrConflict         = errors.New("platform config conflict")
	keyPattern          = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{1,127}$`)
	sensitiveKeyPattern = regexp.MustCompile(`(?i)(password|passwd|secret|token|private[_-]?key|credential|access[_-]?key|dsn)`)
)

type Record struct {
	ID            string          `db:"id" json:"id"`
	Key           string          `db:"config_key" json:"key"`
	Name          string          `db:"name" json:"name"`
	Category      string          `db:"category" json:"category"`
	ValueType     string          `db:"value_type" json:"value_type"`
	Value         json.RawMessage `db:"value" json:"value" swaggertype:"object"`
	Description   string          `db:"description" json:"description"`
	IsPublic      bool            `db:"is_public" json:"is_public"`
	Status        string          `db:"status" json:"status"`
	CreatedAt     time.Time       `db:"created_at" json:"created_at"`
	CreatedBy     string          `db:"created_by" json:"created_by"`
	CreatedByName string          `db:"-" json:"created_by_name"`
	UpdatedAt     time.Time       `db:"updated_at" json:"updated_at"`
	UpdatedBy     string          `db:"updated_by" json:"updated_by"`
	UpdatedByName string          `db:"-" json:"updated_by_name"`
	Version       int64           `db:"version" json:"version"`
}
type PublicView struct {
	Key       string          `db:"config_key" json:"key"`
	Name      string          `db:"name" json:"name"`
	Category  string          `db:"category" json:"category"`
	ValueType string          `db:"value_type" json:"value_type"`
	Value     json.RawMessage `db:"value" json:"value" swaggertype:"object"`
}
type Input struct {
	Key, Name, Category, Description, Status string
	Value                                    json.RawMessage
	IsPublic                                 bool
}
type UpdateInput struct {
	ID, Name, Category, Description, Status string
	Value                                   json.RawMessage
	IsPublic                                bool
	Version                                 int64
}
type Page struct {
	Items    []Record `json:"items"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
	Total    int64    `json:"total"`
}
type PageInput struct {
	pagination.Request
	Keyword       string
	IDs           []string
	Categories    []string
	ValueTypes    []string
	Statuses      []string
	IsPublic      *bool
	CreatedAtFrom *time.Time
	CreatedAtTo   *time.Time
}
type Service struct {
	db         *sqlx.DB
	tx         *database.Transactor
	cache      cache.Store
	locker     cache.Locker
	operations operationlog.TransactionalRecorder
	security   securitylog.TransactionalRecorder
	actors     presentation.ActorResolver
	logger     *slog.Logger
	cfg        config.Config
}

func New(db *sqlx.DB, tx *database.Transactor, store cache.Store, locker cache.Locker, operations operationlog.TransactionalRecorder, security securitylog.TransactionalRecorder, actors presentation.ActorResolver, logger *slog.Logger, cfg config.Config) *Service {
	return &Service{db: db, tx: tx, cache: store, locker: locker, operations: operations, security: security, actors: actors, logger: logger, cfg: cfg}
}

const columns = `id,config_key,name,category,value_type,value,description,is_public,status,created_at,created_by,updated_at,updated_by,version`

func validate(key, name, category, description, status string, value json.RawMessage) error {
	if key != "" && (!keyPattern.MatchString(key) || sensitiveKeyPattern.MatchString(key)) {
		return ErrInvalid
	}
	if name == "" || len(name) > maxConfigNameLength || len(category) > maxConfigCategoryLength ||
		(category != "" && !keyPattern.MatchString(category)) || len(description) > maxConfigDescriptionLength ||
		(status != "active" && status != "disabled") || len(value) == 0 || len(value) > maxConfigValueBytes || !json.Valid(value) || containsSensitiveJSONKey(value) {
		return ErrInvalid
	}
	return nil
}

func valueType(value json.RawMessage) string {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "null" {
		return "null"
	}
	if strings.HasPrefix(trimmed, "{") {
		return "object"
	}
	if strings.HasPrefix(trimmed, "[") {
		return "array"
	}
	if strings.HasPrefix(trimmed, `"`) {
		return "string"
	}
	if trimmed == "true" || trimmed == "false" {
		return "boolean"
	}
	return "number"
}

func containsSensitiveJSONKey(value json.RawMessage) bool {
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		return false
	}
	var walk func(any) bool
	walk = func(current any) bool {
		switch item := current.(type) {
		case map[string]any:
			for key, child := range item {
				if sensitiveKeyPattern.MatchString(key) || walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range item {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(decoded)
}
func (s *Service) Create(ctx context.Context, input Input) (Record, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Record{}, err
	}
	input.Key = strings.ToLower(strings.TrimSpace(input.Key))
	input.Name = strings.TrimSpace(input.Name)
	input.Category = strings.ToLower(strings.TrimSpace(input.Category))
	input.Description = strings.TrimSpace(input.Description)
	if err := validate(input.Key, input.Name, input.Category, input.Description, input.Status, input.Value); err != nil {
		return Record{}, err
	}
	generator, err := stableid.New(platformConfigNamespace)
	if err != nil {
		return Record{}, fmt.Errorf("configure platform config stable ID: %w", err)
	}
	id, err := generator.String("platform-config:" + input.Key)
	if err != nil {
		return Record{}, fmt.Errorf("generate platform config stable ID: %w", err)
	}
	err = s.mutate(ctx, "platform.config.create", id, input.Key, configLogRequest(input.Key, input.Name, input.Category, input.IsPublic, input.Status, 0), func(lockCtx context.Context, tx *sqlx.Tx) error {
		now := time.Now()
		var deletedAt sql.NullTime
		findErr := tx.GetContext(lockCtx, &deletedAt, tx.Rebind(`SELECT deleted_at FROM platform_configs WHERE id=? FOR UPDATE`), id)
		switch {
		case findErr == nil && !deletedAt.Valid:
			return ErrConflict
		case findErr == nil:
			_, err := tx.ExecContext(lockCtx, tx.Rebind(`UPDATE platform_configs SET config_key=?,name=?,category=?,value_type=?,value=?,description=?,is_public=?,status=?,deleted_at=NULL,deleted_by=NULL,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND deleted_at IS NOT NULL`), input.Key, input.Name, input.Category, valueType(input.Value), string(input.Value), input.Description, input.IsPublic, input.Status, now, actor.ID, id)
			return err
		case errors.Is(findErr, sql.ErrNoRows):
			_, err := tx.ExecContext(lockCtx, tx.Rebind(`INSERT INTO platform_configs(id,config_key,name,category,value_type,value,description,is_public,status,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,1)`), id, input.Key, input.Name, input.Category, valueType(input.Value), string(input.Value), input.Description, input.IsPublic, input.Status, now, actor.ID, now, actor.ID)
			return err
		default:
			return findErr
		}
	})
	if err != nil {
		return Record{}, err
	}
	return s.Get(ctx, id)
}
func (s *Service) Get(ctx context.Context, id string) (Record, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return Record{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxConfigIDLength {
		return Record{}, ErrInvalid
	}
	var record Record
	err := s.db.GetContext(ctx, &record, s.db.Rebind(`SELECT `+columns+` FROM platform_configs WHERE id=? AND deleted_at IS NULL`), id)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	if err != nil {
		return record, err
	}
	records := []Record{record}
	if err := s.present(ctx, records); err != nil {
		return Record{}, err
	}
	return records[0], nil
}
func (s *Service) Page(ctx context.Context, input PageInput) (Page, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return Page{}, err
	}
	request, err := pagination.Normalize(input.Request)
	if err != nil || len(strings.TrimSpace(input.Keyword)) > maxConfigKeywordLength || len(input.IDs) > 200 || len(input.Categories) > 100 || len(input.ValueTypes) > 10 || len(input.Statuses) > 10 || (input.CreatedAtFrom != nil && input.CreatedAtTo != nil && !input.CreatedAtFrom.Before(*input.CreatedAtTo)) {
		return Page{}, ErrInvalid
	}
	for _, id := range input.IDs {
		if id == "" || id != strings.TrimSpace(id) || len(id) > maxConfigIDLength {
			return Page{}, ErrInvalid
		}
	}
	for _, status := range input.Statuses {
		if status != "active" && status != "disabled" {
			return Page{}, ErrInvalid
		}
	}
	for index := range input.Categories {
		input.Categories[index] = strings.ToLower(strings.TrimSpace(input.Categories[index]))
		if input.Categories[index] == "" || len(input.Categories[index]) > maxConfigCategoryLength || !keyPattern.MatchString(input.Categories[index]) {
			return Page{}, ErrInvalid
		}
	}
	for _, kind := range input.ValueTypes {
		if kind != "string" && kind != "number" && kind != "boolean" && kind != "object" && kind != "array" && kind != "null" {
			return Page{}, ErrInvalid
		}
	}
	where, args := `deleted_at IS NULL`, []any{}
	if keyword := strings.TrimSpace(input.Keyword); keyword != "" {
		where += ` AND (LOWER(config_key) LIKE ? OR LOWER(name) LIKE ? OR LOWER(description) LIKE ?)`
		pattern := "%" + strings.ToLower(keyword) + "%"
		args = append(args, pattern, pattern, pattern)
	}
	for _, filter := range []struct {
		column string
		values []string
	}{{"id", input.IDs}, {"category", input.Categories}, {"value_type", input.ValueTypes}, {"status", input.Statuses}} {
		if len(filter.values) == 0 {
			continue
		}
		clause, inArgs, inErr := sqlx.In(filter.column+` IN (?)`, filter.values)
		if inErr != nil {
			return Page{}, ErrInvalid
		}
		where += " AND " + clause
		args = append(args, inArgs...)
	}
	if input.IsPublic != nil {
		where += ` AND is_public=?`
		args = append(args, *input.IsPublic)
	}
	if input.CreatedAtFrom != nil {
		where += ` AND created_at>=?`
		args = append(args, *input.CreatedAtFrom)
	}
	if input.CreatedAtTo != nil {
		where += ` AND created_at<?`
		args = append(args, *input.CreatedAtTo)
	}
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*) FROM platform_configs WHERE `+where), args...); err != nil {
		return Page{}, err
	}
	queryArgs := append(append([]any{}, args...), request.PageSize, pagination.Offset(request))
	items := []Record{}
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(`SELECT `+columns+` FROM platform_configs WHERE `+where+` ORDER BY category,config_key LIMIT ? OFFSET ?`), queryArgs...); err != nil {
		return Page{}, err
	}
	if err := s.present(ctx, items); err != nil {
		return Page{}, err
	}
	return Page{items, request.Page, request.PageSize, total}, nil
}

func (s *Service) present(ctx context.Context, records []Record) error {
	ids := make([]string, 0, len(records)*2)
	names := make(map[string]string, len(records)*2)
	for _, record := range records {
		for _, id := range []string{record.CreatedBy, record.UpdatedBy} {
			if id = strings.TrimSpace(id); id != "" {
				names[id] = id
				ids = append(ids, id)
			}
		}
	}
	if s.actors != nil {
		resolved, err := s.actors.ResolveUserIDs(ctx, ids)
		if err != nil {
			return err
		}
		for id, name := range resolved {
			names[id] = name
		}
	}
	for index := range records {
		records[index].CreatedByName = names[records[index].CreatedBy]
		records[index].UpdatedByName = names[records[index].UpdatedBy]
		records[index].CreatedAt = presentation.Time(records[index].CreatedAt)
		records[index].UpdatedAt = presentation.Time(records[index].UpdatedAt)
	}
	return nil
}
func (s *Service) Update(ctx context.Context, input UpdateInput) (Record, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Record{}, err
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Category = strings.ToLower(strings.TrimSpace(input.Category))
	input.Description = strings.TrimSpace(input.Description)
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" || len(input.ID) > maxConfigIDLength || input.Version <= 0 {
		return Record{}, ErrInvalid
	}
	if err := validate("", input.Name, input.Category, input.Description, input.Status, input.Value); err != nil {
		return Record{}, err
	}
	existing, err := s.Get(ctx, input.ID)
	if err != nil {
		return Record{}, err
	}
	err = s.mutate(ctx, "platform.config.update", input.ID, existing.Key, configLogRequest(existing.Key, input.Name, input.Category, input.IsPublic, input.Status, input.Version), func(lockCtx context.Context, tx *sqlx.Tx) error {
		result, err := tx.ExecContext(lockCtx, tx.Rebind(`UPDATE platform_configs SET name=?,category=?,value_type=?,value=?,description=?,is_public=?,status=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`), input.Name, input.Category, valueType(input.Value), string(input.Value), input.Description, input.IsPublic, input.Status, time.Now(), actor.ID, input.ID, input.Version)
		if err != nil {
			return err
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("update platform config affected rows: %w", rowsErr)
		}
		if rows != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return s.Get(ctx, input.ID)
}
func (s *Service) Delete(ctx context.Context, id string, version int64) error {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxConfigIDLength || version <= 0 {
		return ErrInvalid
	}
	existing, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	return s.mutate(ctx, "platform.config.delete", id, existing.Key, map[string]any{"version": version}, func(lockCtx context.Context, tx *sqlx.Tx) error {
		now := time.Now()
		result, err := tx.ExecContext(lockCtx, tx.Rebind(`UPDATE platform_configs SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`), now, actor.ID, now, actor.ID, id, version)
		if err != nil {
			return err
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("delete platform config affected rows: %w", rowsErr)
		}
		if rows != 1 {
			return ErrConflict
		}
		return nil
	})
}
func (s *Service) GetPublic(ctx context.Context, key string) (PublicView, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	if !keyPattern.MatchString(key) {
		return PublicView{}, ErrInvalid
	}
	cacheKey := "platform-config:public:v1:" + key
	if s.cache != nil {
		if data, err := s.cache.Get(ctx, cacheKey); err == nil {
			var view PublicView
			if json.Unmarshal(data, &view) == nil {
				return view, nil
			}
		} else if !errors.Is(err, cache.ErrMiss) && s.logger != nil {
			s.logger.WarnContext(ctx, "read public platform config cache", "key", key, "error", err)
		}
	}
	var view PublicView
	err := s.db.GetContext(ctx, &view, s.db.Rebind(`SELECT config_key,name,category,value_type,value FROM platform_configs WHERE LOWER(config_key)=? AND is_public=true AND status='active' AND deleted_at IS NULL`), key)
	if errors.Is(err, sql.ErrNoRows) {
		return view, ErrNotFound
	}
	if err == nil && s.cache != nil {
		if data, e := json.Marshal(view); e == nil {
			if cacheErr := s.cache.Set(ctx, cacheKey, data, s.cfg.PlatformConfig.CacheTTL); cacheErr != nil && s.logger != nil {
				s.logger.WarnContext(ctx, "write public platform config cache", "key", key, "error", cacheErr)
			}
		}
	}
	return view, err
}
func (s *Service) ListPublic(ctx context.Context, category string) ([]PublicView, error) {
	category = strings.ToLower(strings.TrimSpace(category))
	if len(category) > maxConfigCategoryLength || (category != "" && !keyPattern.MatchString(category)) {
		return nil, ErrInvalid
	}
	items := []PublicView{}
	query, args := `SELECT config_key,name,category,value_type,value FROM platform_configs WHERE is_public=true AND status='active' AND deleted_at IS NULL`, []any{}
	if category != "" {
		query += ` AND category=?`
		args = append(args, category)
	}
	query += ` ORDER BY category,config_key LIMIT 1001`
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(query), args...); err != nil {
		return nil, err
	}
	if len(items) > maxPublicConfigs {
		return nil, fmt.Errorf("%w: public platform configuration list exceeds %d records", ErrConflict, maxPublicConfigs)
	}
	return items, nil
}
func (s *Service) mutate(ctx context.Context, operation, id, key string, request any, fn func(context.Context, *sqlx.Tx) error) error {
	run := func(runCtx context.Context) error {
		started := time.Now()
		operationEntry := operationlog.Entry{Operation: operation, ResourceType: "platform_config", ResourceID: id, ResourceName: key, Source: "backend", Protocol: "service", Request: request}
		securityEntry := securitylog.Entry{EventType: securitylog.EventPlatformConfigChanged, SubjectID: id, SubjectName: key, SubjectType: "platform_config", Metadata: map[string]any{"operation": operation, "key": key}}
		err := s.tx.Within(runCtx, nil, func(tx *sqlx.Tx) error {
			if err := fn(runCtx, tx); err != nil {
				return err
			}
			operationEntry.Duration = time.Since(started)
			operationEntry.Succeeded = true
			if s.operations != nil {
				if err := s.operations.RecordTx(runCtx, tx, operationEntry); err != nil {
					return err
				}
			}
			securityEntry.Succeeded = true
			if s.security != nil {
				return s.security.RecordTx(runCtx, tx, securityEntry)
			}
			return nil
		})
		if database.IsUniqueViolation(err) {
			err = ErrConflict
		}
		if err != nil {
			operationEntry.Duration = time.Since(started)
			operationEntry.Succeeded = false
			operationEntry.ErrorCode = "operation_failed"
			operationEntry.ErrorMessage = "operation failed"
			if s.operations != nil {
				_ = s.operations.Record(runCtx, operationEntry)
			}
			securityEntry.Succeeded = false
			securityEntry.ErrorCode = "operation_failed"
			securityEntry.ErrorMessage = "operation failed"
			if s.security != nil {
				_ = s.security.Record(runCtx, securityEntry)
			}
			return err
		}
		if s.cache != nil {
			cacheCtx, cancel := cache.AfterCommitContext(runCtx)
			defer cancel()
			if cacheErr := s.cache.Delete(cacheCtx, "platform-config:public:v1:"+key); cacheErr != nil && s.logger != nil {
				s.logger.WarnContext(cacheCtx, "invalidate public platform config cache", "key", key, "error", cacheErr)
			}
		}
		return nil
	}
	if s.locker != nil {
		var businessErr error
		err := cache.WithLock(ctx, s.locker, "platform-config:"+key, s.cfg.DistributedLock.TTL, s.cfg.DistributedLock.RetryDelay, func(lockCtx context.Context) error {
			businessErr = run(lockCtx)
			return businessErr
		})
		if businessErr != nil {
			return fmt.Errorf("%s: %w", operation, businessErr)
		}
		if err != nil {
			return ErrConflict
		}
	} else if err := run(ctx); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func configLogRequest(key, name, category string, isPublic bool, status string, version int64) map[string]any {
	return map[string]any{"key": key, "name": name, "category": category, "is_public": isPublic, "status": status, "version": version, "value_redacted": true}
}
