package application

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

const columns = `id,code,name,description,icon,home_path,status,sort_order,metadata,created_at,created_by,updated_at,updated_by,version`

type UpdateInput struct {
	ID, Name, Description, Icon, HomePath, Status string
	SortOrder, Version                            int64
	Metadata                                      []byte
}

type PageInput struct {
	pagination.Request
	IDs, Codes, Statuses       []string
	CreatedAtFrom, CreatedAtTo *time.Time
	Sort                       []pagination.Sort
}

type Page = pagination.Result[Record]

type Service struct {
	db         *sqlx.DB
	tx         *database.Transactor
	operations operationlog.TransactionalRecorder
	actors     presentation.ActorResolver
}

func New(db *sqlx.DB, tx *database.Transactor, operations operationlog.TransactionalRecorder, actors presentation.ActorResolver) *Service {
	return &Service{db: db, tx: tx, operations: operations, actors: actors}
}

func (s *Service) Create(ctx context.Context, input Input) (Record, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Record{}, err
	}
	input = Normalize(input)
	if err := Validate(input); err != nil {
		return Record{}, err
	}
	id, err := StableID(input.Code)
	if err != nil {
		return Record{}, err
	}
	err = s.mutate(ctx, "platform.application.create", id, input.Name, input, func(tx *sqlx.Tx) error {
		now := time.Now()
		_, err := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO applications(id,code,name,description,icon,home_path,status,sort_order,metadata,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,1)`), id, input.Code, input.Name, input.Description, input.Icon, input.HomePath, input.Status, input.SortOrder, string(input.Metadata), now, actor.ID, now, actor.ID)
		return err
	})
	if database.IsUniqueViolation(err) {
		return Record{}, ErrConflict
	}
	if err != nil {
		return Record{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) Get(ctx context.Context, id string) (Record, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return Record{}, err
	}
	var record Record
	err := s.db.GetContext(ctx, &record, s.db.Rebind(`SELECT `+columns+` FROM applications WHERE id=? AND deleted_at IS NULL`), strings.TrimSpace(id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
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
	if err != nil || !validPageInput(input, request.Keyword) {
		return Page{}, ErrInvalid
	}
	where := []string{"deleted_at IS NULL"}
	args := []any{}
	if keyword := strings.TrimSpace(request.Keyword); keyword != "" {
		where = append(where, `(LOWER(code) LIKE LOWER(?) OR LOWER(name) LIKE LOWER(?))`)
		value := "%" + keyword + "%"
		args = append(args, value, value)
	}
	var queryErr error
	where, args, queryErr = appendIn(where, args, "id", input.IDs)
	if queryErr == nil {
		where, args, queryErr = appendIn(where, args, "code", input.Codes)
	}
	if queryErr == nil {
		where, args, queryErr = appendIn(where, args, "status", input.Statuses)
	}
	if queryErr != nil {
		return Page{}, ErrInvalid
	}
	if input.CreatedAtFrom != nil {
		where = append(where, "created_at>=?")
		args = append(args, *input.CreatedAtFrom)
	}
	if input.CreatedAtTo != nil {
		where = append(where, "created_at<?")
		args = append(args, *input.CreatedAtTo)
	}
	order, err := applicationOrder(input.Sort)
	if err != nil {
		return Page{}, err
	}
	clause := strings.Join(where, " AND ")
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*) FROM applications WHERE `+clause), args...); err != nil {
		return Page{}, err
	}
	items := []Record{}
	pageArgs := append(append([]any{}, args...), request.PageSize, pagination.Offset(request))
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(`SELECT `+columns+` FROM applications WHERE `+clause+` ORDER BY `+order+` LIMIT ? OFFSET ?`), pageArgs...); err != nil {
		return Page{}, err
	}
	if err := s.present(ctx, items); err != nil {
		return Page{}, err
	}
	return Page{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (Record, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Record{}, err
	}
	current, err := s.Get(ctx, input.ID)
	if err != nil {
		return Record{}, err
	}
	candidate := Normalize(Input{Code: current.Code, Name: input.Name, Description: input.Description, Icon: input.Icon, HomePath: input.HomePath, Status: input.Status, SortOrder: input.SortOrder, Metadata: input.Metadata})
	if input.Version <= 0 || Validate(candidate) != nil {
		return Record{}, ErrInvalid
	}
	err = s.mutate(ctx, "platform.application.update", input.ID, candidate.Name, input, func(tx *sqlx.Tx) error {
		result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE applications SET name=?,description=?,icon=?,home_path=?,status=?,sort_order=?,metadata=?,updated_at=?,updated_by=? WHERE id=? AND version=? AND deleted_at IS NULL`), candidate.Name, candidate.Description, candidate.Icon, candidate.HomePath, candidate.Status, candidate.SortOrder, string(candidate.Metadata), time.Now(), actor.ID, input.ID, input.Version)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
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
	current, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if version <= 0 {
		return ErrInvalid
	}
	return s.mutate(ctx, "platform.application.delete", id, current.Name, map[string]any{"version": version}, func(tx *sqlx.Tx) error {
		var children int
		if err := tx.GetContext(ctx, &children, tx.Rebind(`SELECT count(*) FROM navigations WHERE application_id=? AND deleted_at IS NULL`), id); err != nil {
			return err
		}
		if children > 0 {
			return ErrConflict
		}
		var grants int
		if err := tx.GetContext(ctx, &grants, tx.Rebind(`SELECT count(*) FROM tenant_application_grants WHERE application_id=? AND status='active' AND deleted_at IS NULL`), id); err != nil {
			return err
		}
		if grants > 0 {
			return ErrConflict
		}
		result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE applications SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=? WHERE id=? AND version=? AND deleted_at IS NULL`), time.Now(), actor.ID, time.Now(), actor.ID, id, version)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return ErrConflict
		}
		return nil
	})
}

func (s *Service) mutate(ctx context.Context, operation, id, name string, request any, fn func(*sqlx.Tx) error) error {
	started := time.Now()
	entry := operationlog.Entry{Operation: operation, ResourceType: "application", ResourceID: id, ResourceName: name, Source: "backend", Protocol: "service", Request: request}
	err := s.tx.Within(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if s.operations == nil {
			return nil
		}
		entry.Duration = time.Since(started)
		entry.Succeeded = true
		return s.operations.RecordTx(ctx, tx, entry)
	})
	if err != nil && s.operations != nil {
		entry.Duration = time.Since(started)
		entry.Succeeded = false
		entry.ErrorCode = "operation_failed"
		entry.ErrorMessage = "operation failed"
		_ = s.operations.Record(ctx, entry)
	}
	return err
}

func (s *Service) present(ctx context.Context, records []Record) error {
	ids := []string{}
	for _, record := range records {
		ids = append(ids, record.CreatedBy, record.UpdatedBy)
	}
	names, err := presentation.ActorNames(ctx, s.actors, ids...)
	if err != nil {
		return err
	}
	for index := range records {
		records[index].CreatedByName = names[records[index].CreatedBy]
		records[index].UpdatedByName = names[records[index].UpdatedBy]
		records[index].CreatedAt = presentation.Time(records[index].CreatedAt)
		records[index].UpdatedAt = presentation.Time(records[index].UpdatedAt)
	}
	return nil
}

func validPageInput(input PageInput, keyword string) bool {
	if len(input.IDs) > 200 || len(input.Codes) > 200 || len(input.Statuses) > 2 || len(input.Sort) > 3 || len(strings.TrimSpace(keyword)) > 256 {
		return false
	}
	if input.CreatedAtFrom != nil && input.CreatedAtTo != nil && !input.CreatedAtFrom.Before(*input.CreatedAtTo) {
		return false
	}
	for _, id := range input.IDs {
		if strings.TrimSpace(id) == "" || len(id) > 128 {
			return false
		}
	}
	for _, code := range input.Codes {
		if !codePattern.MatchString(strings.TrimSpace(code)) {
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

func applicationOrder(sorts []pagination.Sort) (string, error) {
	if len(sorts) == 0 {
		return "sort_order ASC,id ASC", nil
	}
	columns := map[string]string{"code": "code", "name": "name", "status": "status", "sort_order": "sort_order", "created_at": "created_at", "updated_at": "updated_at"}
	order := make([]string, 0, len(sorts)+1)
	seen := make(map[string]struct{}, len(sorts))
	for _, sort := range sorts {
		column, ok := columns[sort.Field]
		direction := strings.ToUpper(sort.Direction)
		if !ok || (direction != "ASC" && direction != "DESC") {
			return "", ErrInvalid
		}
		if _, duplicate := seen[column]; duplicate {
			return "", ErrInvalid
		}
		seen[column] = struct{}{}
		order = append(order, column+" "+direction)
	}
	order = append(order, "id ASC")
	return strings.Join(order, ","), nil
}

func appendIn(where []string, args []any, column string, values []string) ([]string, []any, error) {
	if len(values) == 0 {
		return where, args, nil
	}
	query, inArgs, err := sqlx.In(column+` IN (?)`, values)
	if err != nil {
		return nil, nil, err
	}
	return append(where, query), append(args, inArgs...), nil
}
