package resourcecatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrInvalid      = errors.New("invalid resource catalog entry")
	ErrNotFound     = errors.New("resource catalog entry not found")
	ErrConflict     = errors.New("resource catalog entry conflict")
	ErrStaleVersion = errors.New("resource catalog entry stale version")
	ErrCodeOwned    = errors.New("code-owned resource catalog entry is read-only")
	ErrInUse        = errors.New("resource catalog entry is in use")
)

type Resource struct {
	ID          string                       `db:"id" json:"id"`
	Key         string                       `db:"resource_key" json:"resource_key"`
	Name        string                       `db:"name" json:"name"`
	Description string                       `db:"description" json:"description"`
	Scope       resourceaction.ResourceScope `db:"scope" json:"scope"`
	Source      string                       `db:"source" json:"source"`
	Status      string                       `db:"status" json:"status"`
	CreatedAt   time.Time                    `db:"created_at" json:"created_at"`
	CreatedBy   string                       `db:"created_by" json:"created_by"`
	UpdatedAt   time.Time                    `db:"updated_at" json:"updated_at"`
	UpdatedBy   string                       `db:"updated_by" json:"updated_by"`
	Version     int64                        `db:"version" json:"version"`
}

type Action struct {
	ID           string    `db:"id" json:"id"`
	ResourceID   string    `db:"resource_id" json:"resource_id"`
	ResourceKey  string    `db:"resource_key" json:"resource_key"`
	ResourceName string    `db:"resource_name" json:"resource_name"`
	Key          string    `db:"action_key" json:"action_key"`
	Name         string    `db:"name" json:"name"`
	Description  string    `db:"description" json:"description"`
	Source       string    `db:"source" json:"source"`
	Status       string    `db:"status" json:"status"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
	CreatedBy    string    `db:"created_by" json:"created_by"`
	UpdatedAt    time.Time `db:"updated_at" json:"updated_at"`
	UpdatedBy    string    `db:"updated_by" json:"updated_by"`
	Version      int64     `db:"version" json:"version"`
}

type ResourceInput struct {
	Key, Name, Description string
	Scope                  resourceaction.ResourceScope
	Status                 string
}
type ActionInput struct{ ResourceID, Key, Name, Description, Status string }
type PageInput struct {
	pagination.Request
	IDs, Statuses, Sources []string
	ResourceID             string
	ResourceKey            string
	Scope                  resourceaction.ResourceScope
	ActiveResourcesOnly    bool
}
type ResourcePage struct {
	Items    []Resource `json:"items"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
	Total    int64      `json:"total"`
}

type ActionPage struct {
	Items    []Action `json:"items"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
	Total    int64    `json:"total"`
}

type Service struct {
	db         *sqlx.DB
	tx         *database.Transactor
	operations operationlog.TransactionalRecorder
	security   securitylog.TransactionalRecorder
}

func NewService(db *sqlx.DB, tx *database.Transactor, operations operationlog.TransactionalRecorder, security securitylog.TransactionalRecorder) *Service {
	return &Service{db: db, tx: tx, operations: operations, security: security}
}

func validateResource(input ResourceInput) error {
	if !resourceaction.ValidResourceKey(input.Key) || strings.TrimSpace(input.Name) == "" || len(input.Name) > 256 || len(input.Description) > 4096 ||
		(input.Scope != resourceaction.ResourceScopePlatform && input.Scope != resourceaction.ResourceScopeTenant && input.Scope != resourceaction.ResourceScopePrincipal) ||
		(input.Status != "active" && input.Status != "disabled") {
		return ErrInvalid
	}
	return nil
}

func validateAction(input ActionInput) error {
	if _, err := uuid.Parse(input.ResourceID); err != nil {
		return ErrInvalid
	}
	if !resourceaction.ValidActionKey(input.Key) || strings.TrimSpace(input.Name) == "" || len(input.Name) > 256 || len(input.Description) > 4096 ||
		(input.Status != "active" && input.Status != "disabled") {
		return ErrInvalid
	}
	return nil
}

func normalizeResource(input ResourceInput) ResourceInput {
	input.Key = strings.TrimSpace(input.Key)
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	return input
}
func normalizeAction(input ActionInput) ActionInput {
	input.ResourceID = strings.TrimSpace(input.ResourceID)
	input.Key = strings.TrimSpace(input.Key)
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	return input
}

func (s *Service) CreateResource(ctx context.Context, input ResourceInput) (Resource, error) {
	input = normalizeResource(input)
	if err := validateResource(input); err != nil {
		return Resource{}, err
	}
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Resource{}, err
	}
	id := uuid.NewString()
	now := time.Now()
	var created Resource
	err = s.mutate(ctx, "authorization.resource.create", id, input.Name, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO authorization_resources(id,resource_key,name,description,scope,source,status,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,'manual',?,?,?,?,?,1)`), id, input.Key, input.Name, input.Description, input.Scope, input.Status, now, actor.ID, now, actor.ID)
		if err != nil {
			return err
		}
		return tx.GetContext(ctx, &created, tx.Rebind(resourceSelect+` WHERE id=? AND deleted_at IS NULL`), id)
	})
	if err != nil {
		return Resource{}, err
	}
	return created, nil
}

func (s *Service) GetResource(ctx context.Context, id string) (Resource, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Resource{}, ErrInvalid
	}
	var result Resource
	err := s.db.GetContext(ctx, &result, s.db.Rebind(resourceSelect+` WHERE id=? AND deleted_at IS NULL`), id)
	if errors.Is(err, sql.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	return result, err
}

func (s *Service) PageResources(ctx context.Context, input PageInput) (ResourcePage, error) {
	request, err := pagination.Normalize(input.Request)
	if err != nil || !validPage(input) || input.ResourceID != "" || input.ActiveResourcesOnly {
		return ResourcePage{}, ErrInvalid
	}
	where, args := pageConditions(input, "id", "resource_key")
	if input.ResourceKey != "" {
		where += " AND resource_key=?"
		args = append(args, input.ResourceKey)
	}
	if input.Scope != "" {
		where += " AND scope=?"
		args = append(args, input.Scope)
	}
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*) FROM authorization_resources WHERE `+where), args...); err != nil {
		return ResourcePage{}, err
	}
	rows := []Resource{}
	args = append(args, request.PageSize, pagination.Offset(request))
	if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(`SELECT id,resource_key,name,description,scope,source,status,created_at,created_by,updated_at,updated_by,version FROM authorization_resources WHERE `+where+` ORDER BY resource_key,id LIMIT ? OFFSET ?`), args...); err != nil {
		return ResourcePage{}, err
	}
	return ResourcePage{Items: rows, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func (s *Service) UpdateResource(ctx context.Context, id string, version int64, input ResourceInput) (Resource, error) {
	input = normalizeResource(input)
	if _, err := uuid.Parse(id); err != nil || version <= 0 || validateResource(input) != nil {
		return Resource{}, ErrInvalid
	}
	var updated Resource
	err := s.mutate(ctx, "authorization.resource.update", id, input.Name, func(tx *sqlx.Tx) error {
		current, err := lockResource(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Source != "manual" {
			return ErrCodeOwned
		}
		if current.Version != version {
			return ErrStaleVersion
		}
		if current.Key != input.Key || current.Scope != input.Scope {
			return ErrInvalid
		}
		res, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE authorization_resources SET name=?,description=?,status=? WHERE id=? AND version=? AND source='manual' AND deleted_at IS NULL`), input.Name, input.Description, input.Status, id, version)
		if err := affected(res, err); err != nil {
			return err
		}
		return tx.GetContext(ctx, &updated, tx.Rebind(resourceSelect+` WHERE id=? AND deleted_at IS NULL`), id)
	})
	if err != nil {
		return Resource{}, err
	}
	return updated, nil
}

func (s *Service) DeleteResource(ctx context.Context, id string, version int64) error {
	if _, err := uuid.Parse(id); err != nil || version <= 0 {
		return ErrInvalid
	}
	return s.mutate(ctx, "authorization.resource.delete", id, id, func(tx *sqlx.Tx) error {
		current, err := lockResource(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Source != "manual" {
			return ErrCodeOwned
		}
		if current.Version != version {
			return ErrStaleVersion
		}
		var count int
		if err := tx.GetContext(ctx, &count, tx.Rebind(`SELECT count(*) FROM authorization_resource_actions WHERE resource_id=? AND deleted_at IS NULL`), id); err != nil {
			return err
		}
		if count > 0 {
			return ErrInUse
		}
		res, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE authorization_resources SET deleted_at=CURRENT_TIMESTAMP WHERE id=? AND version=? AND source='manual' AND deleted_at IS NULL`), id, version)
		return affected(res, err)
	})
}

func (s *Service) CreateAction(ctx context.Context, input ActionInput) (Action, error) {
	input = normalizeAction(input)
	if err := validateAction(input); err != nil {
		return Action{}, err
	}
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Action{}, err
	}
	id := uuid.NewString()
	now := time.Now()
	var created Action
	err = s.mutate(ctx, "authorization.action.create", id, input.Name, func(tx *sqlx.Tx) error {
		resource, err := lockResource(ctx, tx, input.ResourceID)
		if err != nil {
			return err
		}
		if resource.Status != "active" {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, tx.Rebind(`INSERT INTO authorization_resource_actions(id,resource_id,action_key,name,description,source,status,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,'manual',?,?,?,?,?,1)`), id, input.ResourceID, input.Key, input.Name, input.Description, input.Status, now, actor.ID, now, actor.ID)
		if err != nil {
			return err
		}
		return tx.GetContext(ctx, &created, tx.Rebind(actionSelect+` WHERE a.id=? AND a.deleted_at IS NULL AND r.deleted_at IS NULL`), id)
	})
	if err != nil {
		return Action{}, err
	}
	return created, nil
}

func (s *Service) GetAction(ctx context.Context, id string) (Action, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Action{}, ErrInvalid
	}
	var row Action
	err := s.db.GetContext(ctx, &row, s.db.Rebind(actionSelect+` WHERE a.id=? AND a.deleted_at IS NULL AND r.deleted_at IS NULL`), id)
	if errors.Is(err, sql.ErrNoRows) {
		return Action{}, ErrNotFound
	}
	return row, err
}

func (s *Service) PageActions(ctx context.Context, input PageInput) (ActionPage, error) {
	request, err := pagination.Normalize(input.Request)
	if err != nil || !validPage(input) {
		return ActionPage{}, ErrInvalid
	}
	if input.ResourceID != "" {
		if _, err := uuid.Parse(input.ResourceID); err != nil {
			return ActionPage{}, ErrInvalid
		}
	}
	where, args := pageConditions(input, "a.id", "a.action_key", "r.resource_key", "r.name")
	where += " AND r.deleted_at IS NULL"
	if input.ResourceKey != "" {
		where += " AND r.resource_key=?"
		args = append(args, input.ResourceKey)
	}
	if input.Scope != "" {
		where += " AND r.scope=?"
		args = append(args, input.Scope)
	}
	if input.ActiveResourcesOnly {
		where += " AND r.status='active'"
	}
	if input.ResourceID != "" {
		where += " AND a.resource_id=?"
		args = append(args, input.ResourceID)
	}
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*) FROM authorization_resource_actions a JOIN authorization_resources r ON r.id=a.resource_id WHERE `+where), args...); err != nil {
		return ActionPage{}, err
	}
	rows := []Action{}
	args = append(args, request.PageSize, pagination.Offset(request))
	if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(actionSelect+` WHERE `+where+` ORDER BY r.resource_key,a.action_key,a.id LIMIT ? OFFSET ?`), args...); err != nil {
		return ActionPage{}, err
	}
	return ActionPage{Items: rows, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func (s *Service) UpdateAction(ctx context.Context, id string, version int64, input ActionInput) (Action, error) {
	input = normalizeAction(input)
	if _, err := uuid.Parse(id); err != nil || version <= 0 || validateAction(input) != nil {
		return Action{}, ErrInvalid
	}
	var updated Action
	err := s.mutate(ctx, "authorization.action.update", id, input.Name, func(tx *sqlx.Tx) error {
		current, err := lockAction(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Source != "manual" {
			return ErrCodeOwned
		}
		if current.Version != version {
			return ErrStaleVersion
		}
		if current.ResourceID != input.ResourceID || current.Key != input.Key {
			return ErrInvalid
		}
		resource, err := lockResource(ctx, tx, input.ResourceID)
		if err != nil {
			return err
		}
		if resource.Status != "active" {
			return ErrConflict
		}
		res, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE authorization_resource_actions SET name=?,description=?,status=? WHERE id=? AND version=? AND source='manual' AND deleted_at IS NULL`), input.Name, input.Description, input.Status, id, version)
		if err := affected(res, err); err != nil {
			return err
		}
		return tx.GetContext(ctx, &updated, tx.Rebind(actionSelect+` WHERE a.id=? AND a.deleted_at IS NULL AND r.deleted_at IS NULL`), id)
	})
	if err != nil {
		return Action{}, err
	}
	return updated, nil
}

func (s *Service) DeleteAction(ctx context.Context, id string, version int64) error {
	if _, err := uuid.Parse(id); err != nil || version <= 0 {
		return ErrInvalid
	}
	return s.mutate(ctx, "authorization.action.delete", id, id, func(tx *sqlx.Tx) error {
		current, err := lockAction(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Source != "manual" {
			return ErrCodeOwned
		}
		if current.Version != version {
			return ErrStaleVersion
		}
		var count int
		if err := tx.GetContext(ctx, &count, tx.Rebind(`SELECT count(*) FROM permission_resource_actions WHERE action_id=? AND deleted_at IS NULL`), id); err != nil {
			return err
		}
		if count > 0 {
			return ErrInUse
		}
		res, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE authorization_resource_actions SET deleted_at=CURRENT_TIMESTAMP WHERE id=? AND version=? AND source='manual' AND deleted_at IS NULL`), id, version)
		return affected(res, err)
	})
}

const resourceSelect = `SELECT id,resource_key,name,description,scope,source,status,created_at,created_by,updated_at,updated_by,version FROM authorization_resources`
const actionSelect = `SELECT a.id,a.resource_id,r.resource_key,r.name AS resource_name,a.action_key,a.name,a.description,a.source,a.status,a.created_at,a.created_by,a.updated_at,a.updated_by,a.version FROM authorization_resource_actions a JOIN authorization_resources r ON r.id=a.resource_id`

func lockResource(ctx context.Context, tx *sqlx.Tx, id string) (Resource, error) {
	var row Resource
	err := tx.GetContext(ctx, &row, tx.Rebind(`SELECT id,resource_key,name,description,scope,source,status,created_at,created_by,updated_at,updated_by,version FROM authorization_resources WHERE id=? AND deleted_at IS NULL FOR UPDATE`), id)
	if errors.Is(err, sql.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	return row, err
}
func lockAction(ctx context.Context, tx *sqlx.Tx, id string) (Action, error) {
	var row Action
	err := tx.GetContext(ctx, &row, tx.Rebind(actionSelect+` WHERE a.id=? AND a.deleted_at IS NULL AND r.deleted_at IS NULL FOR UPDATE`), id)
	if errors.Is(err, sql.ErrNoRows) {
		return Action{}, ErrNotFound
	}
	return row, err
}

func affected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrStaleVersion
	}
	return nil
}

func validPage(input PageInput) bool {
	if len(input.Keyword) > 256 || len(input.IDs) > 200 || len(input.Statuses) > 2 || len(input.Sources) > 2 {
		return false
	}
	if input.ResourceKey != "" && !resourceaction.ValidResourceKey(input.ResourceKey) {
		return false
	}
	if input.Scope != "" && input.Scope != resourceaction.ResourceScopePlatform && input.Scope != resourceaction.ResourceScopeTenant && input.Scope != resourceaction.ResourceScopePrincipal {
		return false
	}
	for _, id := range input.IDs {
		if _, err := uuid.Parse(id); err != nil {
			return false
		}
	}
	for _, v := range input.Statuses {
		if v != "active" && v != "disabled" {
			return false
		}
	}
	for _, v := range input.Sources {
		if v != "code" && v != "manual" {
			return false
		}
	}
	return true
}
func pageConditions(input PageInput, idColumn, keyColumn string, extraKeywordColumns ...string) (string, []any) {
	alias := ""
	if strings.Contains(idColumn, ".") {
		alias = strings.SplitN(idColumn, ".", 2)[0] + "."
	}
	where := alias + "deleted_at IS NULL"
	args := []any{}
	if keyword := strings.TrimSpace(input.Keyword); keyword != "" {
		// All columns come from fixed repository call sites, never from request data.
		columns := append([]string{keyColumn, alias + "name"}, extraKeywordColumns...)
		predicates := make([]string, 0, len(columns))
		like := database.LikeContainsPattern(keyword)
		for _, column := range columns {
			predicates = append(predicates, "LOWER("+column+") LIKE LOWER(?) ESCAPE '!'")
			args = append(args, like)
		}
		where += " AND (" + strings.Join(predicates, " OR ") + ")"
	}
	if len(input.IDs) > 0 {
		query, in, err := sqlx.In(idColumn+` IN (?)`, input.IDs)
		if err == nil {
			where += " AND " + query
			args = append(args, in...)
		}
	}
	if len(input.Statuses) > 0 {
		query, in, err := sqlx.In(alias+`status IN (?)`, input.Statuses)
		if err == nil {
			where += " AND " + query
			args = append(args, in...)
		}
	}
	if len(input.Sources) > 0 {
		query, in, err := sqlx.In(alias+`source IN (?)`, input.Sources)
		if err == nil {
			where += " AND " + query
			args = append(args, in...)
		}
	}
	return where, args
}

func (s *Service) mutate(ctx context.Context, name, id, display string, fn func(*sqlx.Tx) error) error {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return err
	}
	start := time.Now()
	entry := operationlog.Entry{Operation: name, ResourceType: "authorization_catalog", ResourceID: id, ResourceName: display, Source: "backend", Protocol: "service"}
	securityEntry := securitylog.Entry{EventType: securitylog.EventPermissionChanged, SubjectID: id, SubjectName: display, SubjectType: "authorization_catalog", Metadata: map[string]any{"operation": name}}
	err := s.tx.Within(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		entry.Duration = time.Since(start)
		entry.Succeeded = true
		if s.operations != nil {
			if err := s.operations.RecordTx(ctx, tx, entry); err != nil {
				return err
			}
		}
		securityEntry.Succeeded = true
		if s.security != nil {
			return s.security.RecordTx(ctx, tx, securityEntry)
		}
		return nil
	})
	if database.IsUniqueViolation(err) {
		return ErrConflict
	}
	if err != nil {
		entry.Duration = time.Since(start)
		entry.Succeeded = false
		entry.ErrorCode = "operation_failed"
		entry.ErrorMessage = "operation failed"
		if s.operations != nil {
			_ = s.operations.Record(ctx, entry)
		}
		securityEntry.Succeeded = false
		securityEntry.ErrorCode = "operation_failed"
		securityEntry.ErrorMessage = "operation failed"
		if s.security != nil {
			_ = s.security.Record(ctx, securityEntry)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
