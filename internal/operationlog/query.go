package operationlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var ErrNotFound = errors.New("operation log not found")

type Record struct {
	ID             string          `db:"id" json:"id"`
	TenantID       string          `db:"tenant_id" json:"tenant_id"`
	ActorID        string          `db:"actor_id" json:"actor_id"`
	ActorType      string          `db:"actor_type" json:"actor_type"`
	ApplicationID  string          `db:"application_id" json:"application_id"`
	Source         string          `db:"source" json:"source"`
	Operation      string          `db:"operation" json:"operation"`
	ResourceType   string          `db:"resource_type" json:"resource_type"`
	ResourceID     string          `db:"resource_id" json:"resource_id"`
	Protocol       string          `db:"protocol" json:"protocol"`
	Method         string          `db:"method" json:"method"`
	Route          string          `db:"route" json:"route"`
	RequestPayload string          `db:"request_payload" json:"request_payload"`
	DurationMS     int64           `db:"duration_ms" json:"duration_ms"`
	Succeeded      bool            `db:"succeeded" json:"succeeded"`
	ErrorCode      string          `db:"error_code" json:"error_code"`
	ErrorMessage   string          `db:"error_message" json:"error_message"`
	RequestID      string          `db:"request_id" json:"request_id"`
	TraceID        string          `db:"trace_id" json:"trace_id"`
	ClientIP       string          `db:"client_ip" json:"client_ip"`
	UserAgent      string          `db:"user_agent" json:"user_agent"`
	Extension      json.RawMessage `db:"extension" json:"extension" swaggertype:"object"`
	OccurredAt     time.Time       `db:"occurred_at" json:"occurred_at"`
	CreatedAt      time.Time       `db:"created_at" json:"created_at"`
	CreatedBy      string          `db:"created_by" json:"created_by"`
	UpdatedAt      time.Time       `db:"updated_at" json:"updated_at"`
	UpdatedBy      string          `db:"updated_by" json:"updated_by"`
	Version        int64           `db:"version" json:"version"`
}

type PageInput struct {
	pagination.Request
	Keyword        string
	IDs            []string
	TenantIDs      []string
	ActorIDs       []string
	ApplicationIDs []string
	Operations     []string
	ResourceTypes  []string
	ResourceIDs    []string
	Sources        []string
	Protocols      []string
	RequestIDs     []string
	Succeeded      *bool
	OccurredAtFrom *time.Time
	OccurredAtTo   *time.Time
}

type Page struct {
	Items    []Record `json:"items"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
	Total    int64    `json:"total"`
}

const recordColumns = `id,tenant_id,actor_id,actor_type,application_id,source,operation,resource_type,resource_id,protocol,method,route,request_payload,duration_ms,succeeded,error_code,error_message,request_id,trace_id,client_ip,user_agent,extension,occurred_at,created_at,created_by,updated_at,updated_by,version`

func (s *Service) Get(ctx context.Context, id string) (Record, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Record{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Record{}, ErrInvalidEntry
	}
	where, args := "id=? AND deleted_at IS NULL", []any{id}
	if actor.TenantID != "" {
		where += " AND tenant_id=?"
		args = append(args, actor.TenantID)
	}
	var record Record
	if err := s.db.GetContext(ctx, &record, s.db.Rebind(`SELECT `+recordColumns+` FROM operation_logs WHERE `+where), args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, ErrNotFound
		}
		return Record{}, err
	}
	if err := s.Record(ctx, Entry{Operation: "operation_log.get", ResourceType: "operation_log", ResourceID: id, Source: "backend", Protocol: "service", Succeeded: true}); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Service) Page(ctx context.Context, input PageInput) (Page, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Page{}, err
	}
	request, err := pagination.Normalize(input.Request)
	if err != nil || invalidPageInput(input) {
		return Page{}, ErrInvalidEntry
	}
	where, args := "deleted_at IS NULL", []any{}
	if actor.TenantID != "" {
		where += " AND tenant_id=?"
		args = append(args, actor.TenantID)
	} else if len(input.TenantIDs) > 0 {
		where, args, err = appendIn(where, args, "tenant_id", input.TenantIDs)
		if err != nil {
			return Page{}, ErrInvalidEntry
		}
	}
	if keyword := strings.TrimSpace(input.Keyword); keyword != "" {
		pattern := "%" + strings.ToLower(keyword) + "%"
		where += ` AND (LOWER(operation) LIKE ? OR LOWER(resource_type) LIKE ? OR LOWER(resource_id) LIKE ? OR LOWER(actor_id) LIKE ? OR LOWER(request_id) LIKE ?)`
		args = append(args, pattern, pattern, pattern, pattern, pattern)
	}
	for _, filter := range []struct {
		column string
		values []string
	}{{"id", input.IDs}, {"actor_id", input.ActorIDs}, {"application_id", input.ApplicationIDs}, {"operation", input.Operations}, {"resource_type", input.ResourceTypes}, {"resource_id", input.ResourceIDs}, {"source", input.Sources}, {"protocol", input.Protocols}, {"request_id", input.RequestIDs}} {
		where, args, err = appendIn(where, args, filter.column, filter.values)
		if err != nil {
			return Page{}, ErrInvalidEntry
		}
	}
	if input.Succeeded != nil {
		where += " AND succeeded=?"
		args = append(args, *input.Succeeded)
	}
	if input.OccurredAtFrom != nil {
		where += " AND occurred_at>=?"
		args = append(args, *input.OccurredAtFrom)
	}
	if input.OccurredAtTo != nil {
		where += " AND occurred_at<?"
		args = append(args, *input.OccurredAtTo)
	}
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*) FROM operation_logs WHERE `+where), args...); err != nil {
		return Page{}, err
	}
	queryArgs := append(append([]any{}, args...), request.PageSize, pagination.Offset(request))
	items := []Record{}
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(`SELECT `+recordColumns+` FROM operation_logs WHERE `+where+` ORDER BY occurred_at DESC,id DESC LIMIT ? OFFSET ?`), queryArgs...); err != nil {
		return Page{}, err
	}
	audit := map[string]any{"page": request.Page, "page_size": request.PageSize, "result_count": len(items), "tenant_filter_count": len(input.TenantIDs), "actor_filter_count": len(input.ActorIDs), "operation_filter_count": len(input.Operations), "has_time_range": input.OccurredAtFrom != nil || input.OccurredAtTo != nil}
	if err := s.Record(ctx, Entry{Operation: "operation_log.page", ResourceType: "operation_log", Source: "backend", Protocol: "service", Request: audit, Succeeded: true}); err != nil {
		return Page{}, err
	}
	return Page{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func invalidPageInput(input PageInput) bool {
	if len(input.Keyword) > 256 || (input.OccurredAtFrom != nil && input.OccurredAtTo != nil && !input.OccurredAtFrom.Before(*input.OccurredAtTo)) {
		return true
	}
	for _, values := range [][]string{input.IDs, input.TenantIDs, input.ActorIDs, input.ApplicationIDs, input.Operations, input.ResourceTypes, input.ResourceIDs, input.Sources, input.Protocols, input.RequestIDs} {
		if len(values) > 200 {
			return true
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || len(value) > 256 {
				return true
			}
		}
	}
	return false
}

func appendIn(where string, args []any, column string, values []string) (string, []any, error) {
	if len(values) == 0 {
		return where, args, nil
	}
	clause, inArgs, err := sqlx.In(column+` IN (?)`, values)
	if err != nil {
		return "", nil, err
	}
	return where + " AND " + clause, append(args, inArgs...), nil
}
