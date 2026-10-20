package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/eventbus"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const definitionColumns = `id,code,name,description,cron_spec,timezone,handler,timeout_seconds,lock_ttl_seconds,status,payload,created_at,created_by,updated_at,updated_by,version`
const runColumns = `id,scheduled_job_id,job_code,job_name,handler,trigger_source,status,request_id,trace_id,started_at,finished_at,duration_ms,error_message,created_at,created_by,updated_at,updated_by,version`

type UpdateDefinitionInput struct {
	ID             string
	Name           string
	Description    string
	CronSpec       string
	Timezone       string
	Handler        string
	TimeoutSeconds int64
	LockTTLSeconds int64
	Status         string
	Payload        []byte
	Version        int64
}

type DefinitionPageInput struct {
	pagination.Request
	IDs           []string
	Codes         []string
	Statuses      []string
	Handlers      []string
	CreatedAtFrom *time.Time
	CreatedAtTo   *time.Time
	Sort          []pagination.Sort
}

type DefinitionPage = pagination.Result[Definition]
type UpdateTarget struct {
	ID             string          `json:"id"`
	Code           string          `json:"code"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	CronSpec       string          `json:"cron_spec"`
	Timezone       string          `json:"timezone"`
	Handler        string          `json:"handler"`
	TimeoutSeconds int64           `json:"timeout_seconds"`
	LockTTLSeconds int64           `json:"lock_ttl_seconds"`
	Status         string          `json:"status"`
	Payload        json.RawMessage `json:"payload" swaggertype:"object"`
	Version        int64           `json:"version"`
}
type DeleteTarget struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Version int64  `json:"version"`
}
type RunPage = pagination.Result[Run]
type RunPageInput struct {
	pagination.Request
	ScheduledJobID string
	Statuses       []string
	TriggerSources []string
	StartedAtFrom  *time.Time
	StartedAtTo    *time.Time
	Sort           []pagination.Sort
}

type DefinitionService struct {
	db         *sqlx.DB
	tx         *database.Transactor
	operations operationlog.TransactionalRecorder
	actors     presentation.ActorResolver
	handlers   *HandlerRegistry
	runtime    *Manager
	outbox     manualOutbox
}

type manualOutbox interface {
	Store(context.Context, *sqlx.Tx, string, *commonv1.EventEnvelope) error
}

func NewDefinitionService(db *sqlx.DB, tx *database.Transactor, operations operationlog.TransactionalRecorder, actors presentation.ActorResolver, handlers *HandlerRegistry, runtime *Manager, outbox *eventbus.Outbox) *DefinitionService {
	return &DefinitionService{db: db, tx: tx, operations: operations, actors: actors, handlers: handlers, runtime: runtime, outbox: outbox}
}

func (s *DefinitionService) Create(ctx context.Context, input DefinitionInput) (Definition, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Definition{}, err
	}
	input = normalizeDefinition(input)
	if err := validateDefinition(input); err != nil {
		return Definition{}, err
	}
	if _, err := s.handlers.Resolve(input.Handler); err != nil {
		return Definition{}, ErrInvalidDefinition
	}
	id := uuid.NewString()
	err = s.mutate(ctx, "platform.scheduled-job.create", id, input.Name, input, func(tx *sqlx.Tx) error {
		now := time.Now()
		_, execErr := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO scheduled_jobs(id,code,name,description,cron_spec,timezone,handler,timeout_seconds,lock_ttl_seconds,status,payload,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1)`), id, input.Code, input.Name, input.Description, input.CronSpec, input.Timezone, input.Handler, input.TimeoutSeconds, input.LockTTLSeconds, input.Status, string(input.Payload), now, actor.ID, now, actor.ID)
		return execErr
	})
	if database.IsUniqueViolation(err) {
		return Definition{}, ErrDefinitionConflict
	}
	if err != nil {
		return Definition{}, err
	}
	s.runtime.Notify()
	return s.Get(ctx, id)
}

func (s *DefinitionService) Get(ctx context.Context, id string) (Definition, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return Definition{}, err
	}
	record, err := s.loadDefinition(ctx, id)
	if err != nil {
		return Definition{}, err
	}
	items := []Definition{record}
	if err := s.present(ctx, items); err != nil {
		return Definition{}, err
	}
	return items[0], nil
}

func (s *DefinitionService) UpdateTarget(ctx context.Context, id string) (UpdateTarget, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return UpdateTarget{}, err
	}
	id = strings.TrimSpace(id)
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return UpdateTarget{}, ErrInvalidDefinition
	}
	definition, err := s.loadDefinition(ctx, id)
	if err != nil {
		return UpdateTarget{}, err
	}
	return UpdateTarget{
		ID: definition.ID, Code: definition.Code, Name: definition.Name,
		Description: definition.Description, CronSpec: definition.CronSpec,
		Timezone: definition.Timezone, Handler: definition.Handler,
		TimeoutSeconds: definition.TimeoutSeconds, LockTTLSeconds: definition.LockTTLSeconds,
		Status: definition.Status, Payload: definition.Payload, Version: definition.Version,
	}, nil
}

func (s *DefinitionService) DeleteTarget(ctx context.Context, id string) (DeleteTarget, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return DeleteTarget{}, err
	}
	id = strings.TrimSpace(id)
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return DeleteTarget{}, ErrInvalidDefinition
	}
	definition, err := s.loadDefinition(ctx, id)
	if err != nil {
		return DeleteTarget{}, err
	}
	return DeleteTarget{
		ID: definition.ID, Code: definition.Code, Name: definition.Name,
		Status: definition.Status, Version: definition.Version,
	}, nil
}

func (s *DefinitionService) loadDefinition(ctx context.Context, id string) (Definition, error) {
	var record Definition
	err := s.db.GetContext(ctx, &record, s.db.Rebind(`SELECT `+definitionColumns+` FROM scheduled_jobs WHERE id=? AND deleted_at IS NULL`), strings.TrimSpace(id))
	if errors.Is(err, sql.ErrNoRows) {
		return Definition{}, ErrDefinitionNotFound
	}
	if err != nil {
		return Definition{}, err
	}
	return record, nil
}

func (s *DefinitionService) Page(ctx context.Context, input DefinitionPageInput) (DefinitionPage, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return DefinitionPage{}, err
	}
	request, err := pagination.Normalize(input.Request)
	if err != nil || !validDefinitionPage(input, request.Keyword) {
		return DefinitionPage{}, ErrInvalidDefinition
	}
	where, args := []string{"deleted_at IS NULL"}, []any{}
	if keyword := strings.TrimSpace(request.Keyword); keyword != "" {
		where = append(where, `(LOWER(code) LIKE LOWER(?) OR LOWER(name) LIKE LOWER(?) OR LOWER(handler) LIKE LOWER(?))`)
		value := "%" + keyword + "%"
		args = append(args, value, value, value)
	}
	for _, filter := range []struct {
		column string
		values []string
	}{{"id", input.IDs}, {"code", input.Codes}, {"status", input.Statuses}, {"handler", input.Handlers}} {
		if len(filter.values) == 0 {
			continue
		}
		query, queryArgs, queryErr := sqlx.In(filter.column+` IN (?)`, filter.values)
		if queryErr != nil {
			return DefinitionPage{}, ErrInvalidDefinition
		}
		where, args = append(where, query), append(args, queryArgs...)
	}
	if input.CreatedAtFrom != nil {
		where = append(where, "created_at>=?")
		args = append(args, *input.CreatedAtFrom)
	}
	if input.CreatedAtTo != nil {
		where = append(where, "created_at<?")
		args = append(args, *input.CreatedAtTo)
	}
	order, err := definitionOrder(input.Sort)
	if err != nil {
		return DefinitionPage{}, err
	}
	clause := strings.Join(where, " AND ")
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*) FROM scheduled_jobs WHERE `+clause), args...); err != nil {
		return DefinitionPage{}, err
	}
	items := []Definition{}
	pageArgs := append(append([]any{}, args...), request.PageSize, pagination.Offset(request))
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(`SELECT `+definitionColumns+` FROM scheduled_jobs WHERE `+clause+` ORDER BY `+order+` LIMIT ? OFFSET ?`), pageArgs...); err != nil {
		return DefinitionPage{}, err
	}
	if err := s.present(ctx, items); err != nil {
		return DefinitionPage{}, err
	}
	return DefinitionPage{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func (s *DefinitionService) Update(ctx context.Context, input UpdateDefinitionInput) (Definition, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Definition{}, err
	}
	current, err := s.Get(ctx, input.ID)
	if err != nil {
		return Definition{}, err
	}
	candidate := normalizeDefinition(DefinitionInput{Code: current.Code, Name: input.Name, Description: input.Description, CronSpec: input.CronSpec, Timezone: input.Timezone, Handler: input.Handler, TimeoutSeconds: input.TimeoutSeconds, LockTTLSeconds: input.LockTTLSeconds, Status: input.Status, Payload: input.Payload})
	if input.Version <= 0 || validateDefinition(candidate) != nil {
		return Definition{}, ErrInvalidDefinition
	}
	if _, err := s.handlers.Resolve(candidate.Handler); err != nil {
		return Definition{}, ErrInvalidDefinition
	}
	err = s.mutate(ctx, "platform.scheduled-job.update", input.ID, candidate.Name, input, func(tx *sqlx.Tx) error {
		result, execErr := tx.ExecContext(ctx, tx.Rebind(`UPDATE scheduled_jobs SET name=?,description=?,cron_spec=?,timezone=?,handler=?,timeout_seconds=?,lock_ttl_seconds=?,status=?,payload=?,updated_at=?,updated_by=? WHERE id=? AND version=? AND deleted_at IS NULL`), candidate.Name, candidate.Description, candidate.CronSpec, candidate.Timezone, candidate.Handler, candidate.TimeoutSeconds, candidate.LockTTLSeconds, candidate.Status, string(candidate.Payload), time.Now(), actor.ID, input.ID, input.Version)
		if execErr != nil {
			return execErr
		}
		affected, execErr := result.RowsAffected()
		if execErr != nil {
			return execErr
		}
		if affected != 1 {
			return ErrDefinitionStaleVersion
		}
		return nil
	})
	if err != nil {
		return Definition{}, err
	}
	s.runtime.Notify()
	return s.Get(ctx, input.ID)
}

func (s *DefinitionService) Delete(ctx context.Context, id string, version int64) error {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return err
	}
	current, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if version <= 0 {
		return ErrInvalidDefinition
	}
	err = s.mutate(ctx, "platform.scheduled-job.delete", id, current.Name, map[string]any{"version": version}, func(tx *sqlx.Tx) error {
		now := time.Now()
		result, execErr := tx.ExecContext(ctx, tx.Rebind(`UPDATE scheduled_jobs SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=? WHERE id=? AND version=? AND deleted_at IS NULL`), now, actor.ID, now, actor.ID, id, version)
		if execErr != nil {
			return execErr
		}
		affected, execErr := result.RowsAffected()
		if execErr != nil {
			return execErr
		}
		if affected != 1 {
			return ErrDefinitionStaleVersion
		}
		return nil
	})
	if err == nil {
		s.runtime.Notify()
	}
	return err
}

type TriggerResult struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

func (s *DefinitionService) Trigger(ctx context.Context, id string) (TriggerResult, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return TriggerResult{}, err
	}
	definition, err := s.loadDefinition(ctx, id)
	if err != nil {
		return TriggerResult{}, err
	}
	if s.outbox == nil {
		return TriggerResult{}, errors.New("scheduled job dispatch outbox is unavailable")
	}
	runID := uuid.NewString()
	requestID, _ := requestid.FromContext(ctx)
	traceID := trace.SpanContextFromContext(ctx).TraceID().String()
	started := time.Now()
	payload, err := json.Marshal(manualDispatchPayload{RunID: runID})
	if err != nil {
		return TriggerResult{}, err
	}
	envelope := &commonv1.EventEnvelope{EventId: runID, EventType: manualDispatchEventType, AggregateId: definition.ID, AggregateType: "scheduled_job", SchemaVersion: 1, OccurredAt: timestamppb.New(started), Context: &commonv1.RequestContext{RequestId: requestID, TraceId: traceID, ActorId: actor.ID, ActorType: string(actor.Type)}, Payload: payload}
	err = s.tx.Within(ctx, &sql.TxOptions{}, func(tx *sqlx.Tx) error {
		_, execErr := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO scheduled_job_runs(id,scheduled_job_id,job_code,job_name,handler,trigger_source,status,request_id,trace_id,started_at,finished_at,duration_ms,error_message,payload,timeout_seconds,lock_ttl_seconds,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,NULL,NULL,0,'',?,?,?,?,?,?,?,1)`), runID, definition.ID, definition.Code, definition.Name, definition.Handler, "manual", "queued", requestID, traceID, string(definition.Payload), definition.TimeoutSeconds, definition.LockTTLSeconds, started, actor.ID, started, actor.ID)
		if execErr != nil {
			return execErr
		}
		if err := s.outbox.Store(ctx, tx, manualDispatchEventType, envelope); err != nil {
			return err
		}
		if s.operations != nil {
			entry := operationlog.Entry{Operation: "platform.scheduled-job.execute", ResourceType: "scheduled_job", ResourceID: definition.ID, ResourceName: definition.Name, Source: "backend", Protocol: "service", Request: map[string]any{"run_id": runID, "handler": definition.Handler}, Duration: time.Since(started), Succeeded: true}
			return s.operations.RecordTx(ctx, tx, entry)
		}
		return nil
	})
	if err != nil {
		return TriggerResult{}, err
	}
	return TriggerResult{RunID: runID, Status: "queued"}, nil
}

func (s *DefinitionService) GetRun(ctx context.Context, id string) (Run, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return Run{}, err
	}
	var record Run
	err := s.db.GetContext(ctx, &record, s.db.Rebind(`SELECT `+runColumns+` FROM scheduled_job_runs WHERE id=? AND deleted_at IS NULL`), strings.TrimSpace(id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrDefinitionNotFound
	}
	if err != nil {
		return Run{}, err
	}
	items := []Run{record}
	if err := s.presentRuns(ctx, items); err != nil {
		return Run{}, err
	}
	return items[0], nil
}

func (s *DefinitionService) PageRuns(ctx context.Context, input RunPageInput) (RunPage, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return RunPage{}, err
	}
	request, err := pagination.Normalize(input.Request)
	if err != nil || strings.TrimSpace(input.ScheduledJobID) == "" || len(input.ScheduledJobID) > 128 || len(input.Statuses) > 5 || len(input.TriggerSources) > 2 || len(input.Sort) > 3 || len(strings.TrimSpace(request.Keyword)) > 256 {
		return RunPage{}, ErrInvalidDefinition
	}
	if input.StartedAtFrom != nil && input.StartedAtTo != nil && !input.StartedAtFrom.Before(*input.StartedAtTo) {
		return RunPage{}, ErrInvalidDefinition
	}
	where, args := []string{"scheduled_job_id=?", "deleted_at IS NULL"}, []any{input.ScheduledJobID}
	if keyword := strings.TrimSpace(request.Keyword); keyword != "" {
		where = append(where, `(LOWER(request_id) LIKE LOWER(?) OR LOWER(trace_id) LIKE LOWER(?))`)
		value := "%" + keyword + "%"
		args = append(args, value, value)
	}
	for _, filter := range []struct {
		column string
		values []string
	}{{"status", input.Statuses}, {"trigger_source", input.TriggerSources}} {
		if len(filter.values) == 0 {
			continue
		}
		query, queryArgs, queryErr := sqlx.In(filter.column+` IN (?)`, filter.values)
		if queryErr != nil {
			return RunPage{}, ErrInvalidDefinition
		}
		where, args = append(where, query), append(args, queryArgs...)
	}
	for _, status := range input.Statuses {
		if status != "queued" && status != "running" && status != "success" && status != "error" && status != "skipped" {
			return RunPage{}, ErrInvalidDefinition
		}
	}
	for _, source := range input.TriggerSources {
		if source != "cron" && source != "manual" {
			return RunPage{}, ErrInvalidDefinition
		}
	}
	if input.StartedAtFrom != nil {
		where = append(where, "started_at>=?")
		args = append(args, *input.StartedAtFrom)
	}
	if input.StartedAtTo != nil {
		where = append(where, "started_at<?")
		args = append(args, *input.StartedAtTo)
	}
	order, err := runOrder(input.Sort)
	if err != nil {
		return RunPage{}, err
	}
	clause := strings.Join(where, " AND ")
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*) FROM scheduled_job_runs WHERE `+clause), args...); err != nil {
		return RunPage{}, err
	}
	items := []Run{}
	pageArgs := append(append([]any{}, args...), request.PageSize, pagination.Offset(request))
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(`SELECT `+runColumns+` FROM scheduled_job_runs WHERE `+clause+` ORDER BY `+order+` LIMIT ? OFFSET ?`), pageArgs...); err != nil {
		return RunPage{}, err
	}
	if err := s.presentRuns(ctx, items); err != nil {
		return RunPage{}, err
	}
	return RunPage{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func (s *DefinitionService) mutate(ctx context.Context, operation, id, name string, request any, fn func(*sqlx.Tx) error) error {
	started := time.Now()
	entry := operationlog.Entry{Operation: operation, ResourceType: "scheduled_job", ResourceID: id, ResourceName: name, Source: "backend", Protocol: "service", Request: request}
	err := s.tx.Within(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if s.operations == nil {
			return nil
		}
		entry.Duration, entry.Succeeded = time.Since(started), true
		return s.operations.RecordTx(ctx, tx, entry)
	})
	if err != nil && s.operations != nil {
		entry.Duration, entry.Succeeded, entry.ErrorCode, entry.ErrorMessage = time.Since(started), false, "operation_failed", "operation failed"
		_ = s.operations.Record(ctx, entry)
	}
	return err
}

func (s *DefinitionService) present(ctx context.Context, items []Definition) error {
	ids := make([]string, 0, len(items)*2)
	for _, item := range items {
		ids = append(ids, item.CreatedBy, item.UpdatedBy)
	}
	names, err := presentation.ActorNames(ctx, s.actors, ids...)
	if err != nil {
		return err
	}
	for index := range items {
		items[index].CreatedByName = names[items[index].CreatedBy]
		items[index].UpdatedByName = names[items[index].UpdatedBy]
		items[index].CreatedAt = presentation.Time(items[index].CreatedAt)
		items[index].UpdatedAt = presentation.Time(items[index].UpdatedAt)
	}
	return nil
}

func (s *DefinitionService) presentRuns(ctx context.Context, items []Run) error {
	ids := make([]string, 0, len(items)*2)
	for _, item := range items {
		ids = append(ids, item.CreatedBy, item.UpdatedBy)
	}
	names, err := presentation.ActorNames(ctx, s.actors, ids...)
	if err != nil {
		return err
	}
	for index := range items {
		items[index].CreatedByName, items[index].UpdatedByName = names[items[index].CreatedBy], names[items[index].UpdatedBy]
		if items[index].StartedAt != nil {
			value := presentation.Time(*items[index].StartedAt)
			items[index].StartedAt = &value
		}
		if items[index].FinishedAt != nil {
			value := presentation.Time(*items[index].FinishedAt)
			items[index].FinishedAt = &value
		}
		items[index].CreatedAt, items[index].UpdatedAt = presentation.Time(items[index].CreatedAt), presentation.Time(items[index].UpdatedAt)
	}
	return nil
}

func validDefinitionPage(input DefinitionPageInput, keyword string) bool {
	if len(input.IDs) > 200 || len(input.Codes) > 200 || len(input.Statuses) > 2 || len(input.Handlers) > 200 || len(input.Sort) > 3 || len(strings.TrimSpace(keyword)) > 256 {
		return false
	}
	if input.CreatedAtFrom != nil && input.CreatedAtTo != nil && !input.CreatedAtFrom.Before(*input.CreatedAtTo) {
		return false
	}
	for _, value := range append(append(append([]string{}, input.IDs...), input.Codes...), input.Handlers...) {
		if strings.TrimSpace(value) == "" || len(value) > 128 {
			return false
		}
	}
	for _, status := range input.Statuses {
		if status != "active" && status != "disabled" {
			return false
		}
	}
	return true
}

func definitionOrder(sorts []pagination.Sort) (string, error) {
	if len(sorts) == 0 {
		return "code ASC,id ASC", nil
	}
	allowed := map[string]string{"code": "code", "name": "name", "handler": "handler", "status": "status", "created_at": "created_at", "updated_at": "updated_at"}
	order, seen := make([]string, 0, len(sorts)+1), map[string]struct{}{}
	for _, sort := range sorts {
		column, ok := allowed[sort.Field]
		direction := strings.ToUpper(sort.Direction)
		if !ok || (direction != "ASC" && direction != "DESC") {
			return "", ErrInvalidDefinition
		}
		if _, duplicate := seen[column]; duplicate {
			return "", ErrInvalidDefinition
		}
		seen[column] = struct{}{}
		order = append(order, column+" "+direction)
	}
	return strings.Join(append(order, "id ASC"), ","), nil
}

func runOrder(sorts []pagination.Sort) (string, error) {
	if len(sorts) == 0 {
		return "created_at DESC,id ASC", nil
	}
	allowed := map[string]string{"created_at": "created_at", "started_at": "started_at", "finished_at": "finished_at", "duration_ms": "duration_ms", "status": "status", "trigger_source": "trigger_source"}
	order, seen := make([]string, 0, len(sorts)+1), map[string]struct{}{}
	for _, sort := range sorts {
		column, ok := allowed[sort.Field]
		direction := strings.ToUpper(sort.Direction)
		if !ok || (direction != "ASC" && direction != "DESC") {
			return "", ErrInvalidDefinition
		}
		if _, duplicate := seen[column]; duplicate {
			return "", ErrInvalidDefinition
		}
		seen[column] = struct{}{}
		order = append(order, column+" "+direction)
	}
	return strings.Join(append(order, "id ASC"), ","), nil
}
