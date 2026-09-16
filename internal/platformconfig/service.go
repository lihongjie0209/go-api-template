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

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrInvalid          = errors.New("invalid platform config")
	ErrNotFound         = errors.New("platform config not found")
	ErrConflict         = errors.New("platform config conflict")
	keyPattern          = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{1,127}$`)
	sensitiveKeyPattern = regexp.MustCompile(`(?i)(password|passwd|secret|token|private[_-]?key|credential|access[_-]?key|dsn)`)
)

type Record struct {
	ID          string          `db:"id" json:"id"`
	Key         string          `db:"config_key" json:"key"`
	Name        string          `db:"name" json:"name"`
	Category    string          `db:"category" json:"category"`
	ValueType   string          `db:"value_type" json:"value_type"`
	Value       json.RawMessage `db:"value" json:"value" swaggertype:"object"`
	Description string          `db:"description" json:"description"`
	IsPublic    bool            `db:"is_public" json:"is_public"`
	Status      string          `db:"status" json:"status"`
	CreatedAt   time.Time       `db:"created_at" json:"created_at"`
	CreatedBy   string          `db:"created_by" json:"created_by"`
	UpdatedAt   time.Time       `db:"updated_at" json:"updated_at"`
	UpdatedBy   string          `db:"updated_by" json:"updated_by"`
	Version     int64           `db:"version" json:"version"`
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
	operations operationlog.Recorder
	security   securitylog.Recorder
	logger     *slog.Logger
	cfg        config.Config
}

func New(db *sqlx.DB, tx *database.Transactor, store cache.Store, locker cache.Locker, operations operationlog.Recorder, security securitylog.Recorder, logger *slog.Logger, cfg config.Config) *Service {
	return &Service{db: db, tx: tx, cache: store, locker: locker, operations: operations, security: security, logger: logger, cfg: cfg}
}

const columns = `id,config_key,name,category,value_type,value,description,is_public,status,created_at,created_by,updated_at,updated_by,version`

func validate(key, name, status string, value json.RawMessage) error {
	if key != "" && (!keyPattern.MatchString(key) || sensitiveKeyPattern.MatchString(key)) {
		return ErrInvalid
	}
	if strings.TrimSpace(name) == "" || (status != "active" && status != "disabled") || len(value) == 0 || len(value) > 1<<20 || !json.Valid(value) || containsSensitiveJSONKey(value) {
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
	if err := validate(input.Key, input.Name, input.Status, input.Value); err != nil {
		return Record{}, err
	}
	id := uuid.NewString()
	err = s.mutate(ctx, "platform.config.create", id, input.Key, configLogRequest(input.Key, input.Name, input.Category, input.IsPublic, input.Status, 0), func(lockCtx context.Context, tx *sqlx.Tx) error {
		now := time.Now()
		_, err := tx.ExecContext(lockCtx, tx.Rebind(`INSERT INTO platform_configs(id,config_key,name,category,value_type,value,description,is_public,status,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,1)`), id, input.Key, input.Name, input.Category, valueType(input.Value), string(input.Value), input.Description, input.IsPublic, input.Status, now, actor.ID, now, actor.ID)
		if database.IsUniqueViolation(err) {
			return ErrConflict
		}
		return err
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
	if strings.TrimSpace(id) == "" {
		return Record{}, ErrInvalid
	}
	var record Record
	err := s.db.GetContext(ctx, &record, s.db.Rebind(`SELECT `+columns+` FROM platform_configs WHERE id=? AND deleted_at IS NULL`), id)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	return record, err
}
func (s *Service) Page(ctx context.Context, input PageInput) (Page, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return Page{}, err
	}
	request, err := pagination.Normalize(input.Request)
	if err != nil || len(input.IDs) > 200 || len(input.Categories) > 100 || len(input.ValueTypes) > 10 || len(input.Statuses) > 10 || (input.CreatedAtFrom != nil && input.CreatedAtTo != nil && !input.CreatedAtFrom.Before(*input.CreatedAtTo)) {
		return Page{}, ErrInvalid
	}
	for _, status := range input.Statuses {
		if status != "active" && status != "disabled" {
			return Page{}, ErrInvalid
		}
	}
	for index := range input.Categories {
		input.Categories[index] = strings.ToLower(strings.TrimSpace(input.Categories[index]))
		if input.Categories[index] == "" || len(input.Categories[index]) > 128 {
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
	return Page{items, request.Page, request.PageSize, total}, nil
}
func (s *Service) Update(ctx context.Context, input UpdateInput) (Record, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Record{}, err
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Category = strings.ToLower(strings.TrimSpace(input.Category))
	if input.ID == "" || input.Version <= 0 {
		return Record{}, ErrInvalid
	}
	if err := validate("", input.Name, input.Status, input.Value); err != nil {
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
	if id == "" || version <= 0 {
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
	if len(category) > 128 {
		return nil, ErrInvalid
	}
	items := []PublicView{}
	query, args := `SELECT config_key,name,category,value_type,value FROM platform_configs WHERE is_public=true AND status='active' AND deleted_at IS NULL`, []any{}
	if category != "" {
		query += ` AND category=?`
		args = append(args, category)
	}
	query += ` ORDER BY category,config_key`
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(query), args...); err != nil {
		return nil, err
	}
	return items, nil
}
func (s *Service) mutate(ctx context.Context, operation, id, key string, request any, fn func(context.Context, *sqlx.Tx) error) error {
	committed := false
	run := func(runCtx context.Context) error {
		err := operationlog.Do(runCtx, s.operations, operationlog.Entry{Operation: operation, ResourceType: "platform_config", ResourceID: id, Source: "backend", Protocol: "service", Request: request}, func() error {
			businessErr := s.tx.Within(runCtx, nil, func(tx *sqlx.Tx) error { return fn(runCtx, tx) })
			committed = businessErr == nil
			return businessErr
		})
		if committed && s.cache != nil {
			cacheCtx, cancel := cache.AfterCommitContext(runCtx)
			defer cancel()
			if cacheErr := s.cache.Delete(cacheCtx, "platform-config:public:v1:"+key); cacheErr != nil && s.logger != nil {
				s.logger.WarnContext(cacheCtx, "invalidate public platform config cache", "key", key, "error", cacheErr)
			}
		}
		if s.security == nil {
			return err
		}
		securityErr := s.security.Record(runCtx, securitylog.Entry{EventType: securitylog.EventPlatformConfigChanged, SubjectID: id, SubjectType: "platform_config", Succeeded: committed, Metadata: map[string]any{"operation": operation, "key": key}})
		if securityErr != nil && committed && err == nil && s.security.FailClosed() {
			return securityErr
		}
		return err
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
