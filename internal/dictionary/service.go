package dictionary

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrInvalid             = errors.New("invalid data dictionary")
	ErrNotFound            = errors.New("data dictionary not found")
	ErrConflict            = errors.New("data dictionary conflict")
	ErrProviderUnavailable = errors.New("data dictionary provider unavailable")
	codePattern            = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,127}$`)
)

const definitionColumns = `id,dictionary_code,name,dictionary_type,source_type,description,status,extension,created_at,created_by,updated_at,updated_by,version`
const itemColumns = `id,dictionary_id,parent_id,code,name,value,disabled,sort_order,extension,created_at,created_by,updated_at,updated_by,version`
const maxDescriptionLength = 4096
const maxValueLength = 1 << 20

type Service struct {
	db         *sqlx.DB
	tx         *database.Transactor
	cache      cache.Store
	providers  *ProviderRegistry
	operations operationlog.TransactionalRecorder
	actors     presentation.ActorResolver
	cfg        config.Config
}

func New(db *sqlx.DB, tx *database.Transactor, store cache.Store, providers *ProviderRegistry, operations operationlog.TransactionalRecorder, actors presentation.ActorResolver, cfg config.Config) *Service {
	return &Service{db: db, tx: tx, cache: store, providers: providers, operations: operations, actors: actors, cfg: cfg}
}
func (s *Service) operationEntry(operation, resource, id, name string, request any, started time.Time) operationlog.Entry {
	return operationlog.Entry{Operation: operation, ResourceType: resource, ResourceID: id, ResourceName: name, Source: "backend", Protocol: "service", Request: request, Duration: time.Since(started)}
}
func (s *Service) recordTx(ctx context.Context, tx *sqlx.Tx, operation, resource, id, name string, request any, started time.Time) error {
	if s.operations == nil {
		return nil
	}
	entry := s.operationEntry(operation, resource, id, name, request, started)
	entry.Succeeded = true
	return s.operations.RecordTx(ctx, tx, entry)
}
func (s *Service) recordFailure(ctx context.Context, operation, resource, id, name string, request any, started time.Time) {
	if s.operations == nil {
		return
	}
	entry := s.operationEntry(operation, resource, id, name, request, started)
	entry.ErrorCode = "operation_failed"
	entry.ErrorMessage = "operation failed"
	_ = s.operations.Record(ctx, entry)
}

func validJSON(value json.RawMessage) bool {
	return len(value) == 0 || (len(value) <= 16<<10 && json.Valid(value) && strings.HasPrefix(strings.TrimSpace(string(value)), "{"))
}
func normalizeJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}
func validDefinition(in DefinitionInput) bool {
	return codePattern.MatchString(in.Code) && strings.TrimSpace(in.Name) != "" && len(in.Name) <= 256 && len(in.Description) <= maxDescriptionLength && (in.Kind == KindEnum || in.Kind == KindTree) && (in.Source == SourceStatic || in.Source == SourceProvider) && (in.Status == "active" || in.Status == "disabled") && validJSON(in.Extension)
}

func (s *Service) CreateDefinition(ctx context.Context, in DefinitionInput) (Definition, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Definition{}, err
	}
	in.Code = strings.ToLower(strings.TrimSpace(in.Code))
	in.Name = strings.TrimSpace(in.Name)
	in.Extension = normalizeJSON(in.Extension)
	if !validDefinition(in) {
		return Definition{}, ErrInvalid
	}
	id := uuid.NewString()
	started := time.Now()
	err = s.tx.Within(ctx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		_, e := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO dictionary_definitions(id,dictionary_code,name,dictionary_type,source_type,description,status,extension,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,1)`), id, in.Code, in.Name, in.Kind, in.Source, in.Description, in.Status, string(in.Extension), now, actor.ID, now, actor.ID)
		if e != nil {
			return e
		}
		return s.recordTx(ctx, tx, "dictionary.definition.create", "dictionary", id, in.Name, map[string]any{"code": in.Code, "type": in.Kind, "source": in.Source}, started)
	})
	if err != nil {
		s.recordFailure(ctx, "dictionary.definition.create", "dictionary", id, in.Name, map[string]any{"code": in.Code, "type": in.Kind, "source": in.Source}, started)
		if database.IsUniqueViolation(err) {
			return Definition{}, ErrConflict
		}
		return Definition{}, err
	}
	return s.getDefinition(ctx, id)
}
func (s *Service) getDefinition(ctx context.Context, id string) (Definition, error) {
	var d Definition
	err := s.db.GetContext(ctx, &d, s.db.Rebind(`SELECT `+definitionColumns+` FROM dictionary_definitions WHERE id=? AND deleted_at IS NULL`), id)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err == nil {
		records := []Definition{d}
		err = s.presentDefinitions(ctx, records)
		d = records[0]
	}
	return d, err
}
func (s *Service) presentDefinitions(ctx context.Context, records []Definition) error {
	ids := make([]string, 0, len(records)*2)
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
func (s *Service) presentItems(ctx context.Context, records []Item) error {
	ids := make([]string, 0, len(records)*2)
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
func (s *Service) GetDefinition(ctx context.Context, id string) (Definition, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return Definition{}, err
	}
	if uuid.Validate(id) != nil {
		return Definition{}, ErrInvalid
	}
	return s.getDefinition(ctx, id)
}
func (s *Service) PageDefinitions(ctx context.Context, in DefinitionPageInput) (Page, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return Page{}, err
	}
	page, err := pagination.Normalize(in.Request)
	if err != nil || len(in.Keyword) > 256 || len(in.Types) > 10 || len(in.Sources) > 10 || len(in.Statuses) > 10 {
		return Page{}, ErrInvalid
	}
	for _, value := range in.Types {
		if value != KindEnum && value != KindTree {
			return Page{}, ErrInvalid
		}
	}
	for _, value := range in.Sources {
		if value != SourceStatic && value != SourceProvider {
			return Page{}, ErrInvalid
		}
	}
	for _, value := range in.Statuses {
		if value != "active" && value != "disabled" {
			return Page{}, ErrInvalid
		}
	}
	where := `deleted_at IS NULL`
	args := []any{}
	if in.Keyword != "" {
		where += ` AND (lower(dictionary_code) LIKE ? OR lower(name) LIKE ?)`
		like := "%" + strings.ToLower(in.Keyword) + "%"
		args = append(args, like, like)
	}
	typeStrings := make([]string, len(in.Types))
	for i, v := range in.Types {
		typeStrings[i] = string(v)
	}
	sourceStrings := make([]string, len(in.Sources))
	for i, v := range in.Sources {
		sourceStrings[i] = string(v)
	}
	where, args = appendIn(where, args, "dictionary_type", typeStrings)
	where, args = appendIn(where, args, "source_type", sourceStrings)
	where, args = appendIn(where, args, "status", in.Statuses)
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT COUNT(*) FROM dictionary_definitions WHERE `+where), args...); err != nil {
		return Page{}, err
	}
	queryArgs := append(append([]any{}, args...), page.PageSize, pagination.Offset(page))
	items := []Definition{}
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(`SELECT `+definitionColumns+` FROM dictionary_definitions WHERE `+where+` ORDER BY dictionary_code LIMIT ? OFFSET ?`), queryArgs...); err != nil {
		return Page{}, err
	}
	if err := s.presentDefinitions(ctx, items); err != nil {
		return Page{}, err
	}
	return Page{Items: items, Page: page.Page, PageSize: page.PageSize, Total: total}, nil
}
func (s *Service) GetItem(ctx context.Context, id string) (Item, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return Item{}, err
	}
	if uuid.Validate(id) != nil {
		return Item{}, ErrInvalid
	}
	var item Item
	err := s.db.GetContext(ctx, &item, s.db.Rebind(`SELECT `+itemColumns+` FROM dictionary_items WHERE id=? AND deleted_at IS NULL`), id)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err == nil {
		records := []Item{item}
		err = s.presentItems(ctx, records)
		item = records[0]
	}
	return item, err
}
func (s *Service) PageItems(ctx context.Context, in ItemPageInput) (ItemPage, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return ItemPage{}, err
	}
	page, err := pagination.Normalize(in.Request)
	if err != nil || uuid.Validate(in.DictionaryID) != nil || len(in.Keyword) > 256 {
		return ItemPage{}, ErrInvalid
	}
	if in.ParentID != nil && uuid.Validate(*in.ParentID) != nil {
		return ItemPage{}, ErrInvalid
	}
	where := `dictionary_id=? AND deleted_at IS NULL`
	args := []any{in.DictionaryID}
	if in.Keyword != "" {
		where += ` AND (lower(code) LIKE ? OR lower(name) LIKE ?)`
		like := "%" + strings.ToLower(in.Keyword) + "%"
		args = append(args, like, like)
	}
	if in.Disabled != nil {
		where += ` AND disabled=?`
		args = append(args, *in.Disabled)
	}
	if in.ParentID != nil {
		where += ` AND parent_id=?`
		args = append(args, *in.ParentID)
	}
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT COUNT(*) FROM dictionary_items WHERE `+where), args...); err != nil {
		return ItemPage{}, err
	}
	queryArgs := append(append([]any{}, args...), page.PageSize, pagination.Offset(page))
	items := []Item{}
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(`SELECT `+itemColumns+` FROM dictionary_items WHERE `+where+` ORDER BY sort_order,code LIMIT ? OFFSET ?`), queryArgs...); err != nil {
		return ItemPage{}, err
	}
	if err := s.presentItems(ctx, items); err != nil {
		return ItemPage{}, err
	}
	return ItemPage{Items: items, Page: page.Page, PageSize: page.PageSize, Total: total}, nil
}

func validItem(in ItemInput) bool {
	return uuid.Validate(in.DictionaryID) == nil && codePattern.MatchString(in.Code) && strings.TrimSpace(in.Name) != "" && len(in.Name) <= 256 && len(in.Value) <= maxValueLength && validJSON(in.Extension)
}
func (s *Service) CreateItem(ctx context.Context, in ItemInput) (Item, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Item{}, err
	}
	in.Code = strings.ToLower(strings.TrimSpace(in.Code))
	in.Name = strings.TrimSpace(in.Name)
	in.Extension = normalizeJSON(in.Extension)
	if !validItem(in) {
		return Item{}, ErrInvalid
	}
	id := uuid.NewString()
	started := time.Now()
	err = s.tx.Within(ctx, nil, func(tx *sqlx.Tx) error {
		var definition struct {
			Kind   Kind   `db:"dictionary_type"`
			Source Source `db:"source_type"`
		}
		if e := tx.GetContext(ctx, &definition, tx.Rebind(`SELECT dictionary_type,source_type FROM dictionary_definitions WHERE id=? AND deleted_at IS NULL FOR UPDATE`), in.DictionaryID); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return ErrNotFound
			}
			return e
		}
		if definition.Source != SourceStatic || (definition.Kind == KindEnum && in.ParentID != nil) {
			return ErrInvalid
		}
		if definition.Kind == KindTree {
			var count int
			if e := tx.GetContext(ctx, &count, tx.Rebind(`SELECT COUNT(*) FROM dictionary_items WHERE dictionary_id=? AND deleted_at IS NULL`), in.DictionaryID); e != nil {
				return e
			}
			if count >= s.cfg.Dictionary.MaxTreeNodes {
				return ErrConflict
			}
		}
		if in.ParentID != nil {
			var count int
			if e := tx.GetContext(ctx, &count, tx.Rebind(`SELECT COUNT(*) FROM dictionary_items WHERE id=? AND dictionary_id=? AND deleted_at IS NULL`), *in.ParentID, in.DictionaryID); e != nil || count != 1 {
				return ErrInvalid
			}
		}
		now := time.Now()
		_, e := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO dictionary_items(id,dictionary_id,parent_id,code,name,value,disabled,sort_order,extension,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,1)`), id, in.DictionaryID, in.ParentID, in.Code, in.Name, in.Value, in.Disabled, in.SortOrder, string(in.Extension), now, actor.ID, now, actor.ID)
		if e == nil {
			_, e = tx.ExecContext(ctx, tx.Rebind(`UPDATE dictionary_definitions SET updated_at=?,updated_by=? WHERE id=?`), now, actor.ID, in.DictionaryID)
		}
		if e != nil {
			return e
		}
		return s.recordTx(ctx, tx, "dictionary.item.create", "dictionary_item", id, in.Name, map[string]any{"dictionary_id": in.DictionaryID, "code": in.Code}, started)
	})
	if err != nil {
		s.recordFailure(ctx, "dictionary.item.create", "dictionary_item", id, in.Name, map[string]any{"dictionary_id": in.DictionaryID, "code": in.Code}, started)
		if database.IsUniqueViolation(err) {
			return Item{}, ErrConflict
		}
		return Item{}, err
	}
	var item Item
	err = s.db.GetContext(ctx, &item, s.db.Rebind(`SELECT `+itemColumns+` FROM dictionary_items WHERE id=? AND deleted_at IS NULL`), id)
	if err == nil {
		records := []Item{item}
		err = s.presentItems(ctx, records)
		item = records[0]
	}
	return item, err
}

func (s *Service) UpdateDefinition(ctx context.Context, in DefinitionUpdate) (Definition, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Definition{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Extension = normalizeJSON(in.Extension)
	if uuid.Validate(in.ID) != nil || in.Name == "" || len(in.Name) > 256 || len(in.Description) > maxDescriptionLength || (in.Status != "active" && in.Status != "disabled") || in.Version < 1 || !validJSON(in.Extension) {
		return Definition{}, ErrInvalid
	}
	started := time.Now()
	err = s.tx.Within(ctx, nil, func(tx *sqlx.Tx) error {
		result, e := tx.ExecContext(ctx, tx.Rebind(`UPDATE dictionary_definitions SET name=?,description=?,status=?,extension=?,updated_at=?,updated_by=? WHERE id=? AND version=? AND deleted_at IS NULL`), in.Name, in.Description, in.Status, string(in.Extension), time.Now(), actor.ID, in.ID, in.Version)
		if e != nil {
			return e
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			return ErrConflict
		}
		return s.recordTx(ctx, tx, "dictionary.definition.update", "dictionary", in.ID, in.Name, map[string]any{"status": in.Status, "version": in.Version}, started)
	})
	if err != nil {
		s.recordFailure(ctx, "dictionary.definition.update", "dictionary", in.ID, in.Name, map[string]any{"status": in.Status, "version": in.Version}, started)
		return Definition{}, err
	}
	return s.getDefinition(ctx, in.ID)
}
func (s *Service) DeleteDefinition(ctx context.Context, id string, version int64) error {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return err
	}
	if uuid.Validate(id) != nil || version < 1 {
		return ErrInvalid
	}
	started := time.Now()
	err = s.tx.Within(ctx, nil, func(tx *sqlx.Tx) error {
		var existingVersion int64
		if e := tx.GetContext(ctx, &existingVersion, tx.Rebind(`SELECT version FROM dictionary_definitions WHERE id=? AND deleted_at IS NULL FOR UPDATE`), id); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return ErrNotFound
			}
			return e
		}
		if existingVersion != version {
			return ErrConflict
		}
		var n int
		if e := tx.GetContext(ctx, &n, tx.Rebind(`SELECT COUNT(*) FROM dictionary_items WHERE dictionary_id=? AND deleted_at IS NULL`), id); e != nil {
			return e
		}
		if n > 0 {
			return ErrConflict
		}
		now := time.Now()
		result, e := tx.ExecContext(ctx, tx.Rebind(`UPDATE dictionary_definitions SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=? WHERE id=? AND version=? AND deleted_at IS NULL`), now, actor.ID, now, actor.ID, id, version)
		if e != nil {
			return e
		}
		rows, _ := result.RowsAffected()
		if rows == 0 {
			return ErrConflict
		}
		return s.recordTx(ctx, tx, "dictionary.definition.delete", "dictionary", id, "", map[string]any{"version": version}, started)
	})
	if err != nil {
		s.recordFailure(ctx, "dictionary.definition.delete", "dictionary", id, "", map[string]any{"version": version}, started)
	}
	return err
}
func (s *Service) UpdateItem(ctx context.Context, in ItemUpdate) (Item, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Item{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Extension = normalizeJSON(in.Extension)
	if uuid.Validate(in.ID) != nil || in.Name == "" || len(in.Name) > 256 || len(in.Value) > maxValueLength || in.Version < 1 || !validJSON(in.Extension) || (in.ParentID != nil && (*in.ParentID == in.ID || uuid.Validate(*in.ParentID) != nil)) {
		return Item{}, ErrInvalid
	}
	started := time.Now()
	var dictionaryID string
	err = s.tx.Within(ctx, nil, func(tx *sqlx.Tx) error {
		var currentItem struct {
			DictionaryID string `db:"dictionary_id"`
			Kind         Kind   `db:"dictionary_type"`
		}
		if e := tx.GetContext(ctx, &currentItem, tx.Rebind(`SELECT i.dictionary_id,d.dictionary_type FROM dictionary_items i JOIN dictionary_definitions d ON d.id=i.dictionary_id WHERE i.id=? AND i.deleted_at IS NULL FOR UPDATE`), in.ID); e != nil {
			return e
		}
		dictionaryID = currentItem.DictionaryID
		if currentItem.Kind == KindEnum && in.ParentID != nil {
			return ErrInvalid
		}
		if in.ParentID != nil {
			current := in.ParentID
			for depth := 0; current != nil && depth <= s.cfg.Dictionary.MaxTreeNodes; depth++ {
				if *current == in.ID {
					return ErrInvalid
				}
				var parent struct {
					DictionaryID string  `db:"dictionary_id"`
					ParentID     *string `db:"parent_id"`
				}
				if e := tx.GetContext(ctx, &parent, tx.Rebind(`SELECT dictionary_id,parent_id FROM dictionary_items WHERE id=? AND deleted_at IS NULL`), *current); e != nil || parent.DictionaryID != dictionaryID {
					return ErrInvalid
				}
				current = parent.ParentID
				if depth == s.cfg.Dictionary.MaxTreeNodes {
					return ErrInvalid
				}
			}
		}
		now := time.Now()
		result, e := tx.ExecContext(ctx, tx.Rebind(`UPDATE dictionary_items SET parent_id=?,name=?,value=?,disabled=?,sort_order=?,extension=?,updated_at=?,updated_by=? WHERE id=? AND version=? AND deleted_at IS NULL`), in.ParentID, in.Name, in.Value, in.Disabled, in.SortOrder, string(in.Extension), now, actor.ID, in.ID, in.Version)
		if e != nil {
			return e
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			return ErrConflict
		}
		_, e = tx.ExecContext(ctx, tx.Rebind(`UPDATE dictionary_definitions SET updated_at=?,updated_by=? WHERE id=?`), now, actor.ID, dictionaryID)
		if e != nil {
			return e
		}
		return s.recordTx(ctx, tx, "dictionary.item.update", "dictionary_item", in.ID, in.Name, map[string]any{"version": in.Version}, started)
	})
	if errors.Is(err, sql.ErrNoRows) {
		s.recordFailure(ctx, "dictionary.item.update", "dictionary_item", in.ID, in.Name, map[string]any{"version": in.Version}, started)
		return Item{}, ErrNotFound
	}
	if err != nil {
		s.recordFailure(ctx, "dictionary.item.update", "dictionary_item", in.ID, in.Name, map[string]any{"version": in.Version}, started)
		return Item{}, err
	}
	var item Item
	err = s.db.GetContext(ctx, &item, s.db.Rebind(`SELECT `+itemColumns+` FROM dictionary_items WHERE id=? AND deleted_at IS NULL`), in.ID)
	if err == nil {
		records := []Item{item}
		err = s.presentItems(ctx, records)
		item = records[0]
	}
	return item, err
}
func (s *Service) DeleteItem(ctx context.Context, id string, version int64) error {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return err
	}
	if uuid.Validate(id) != nil || version < 1 {
		return ErrInvalid
	}
	started := time.Now()
	err = s.tx.Within(ctx, nil, func(tx *sqlx.Tx) error {
		var dictionaryID string
		if e := tx.GetContext(ctx, &dictionaryID, tx.Rebind(`SELECT dictionary_id FROM dictionary_items WHERE id=? AND deleted_at IS NULL FOR UPDATE`), id); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return ErrNotFound
			}
			return e
		}
		var children int
		if e := tx.GetContext(ctx, &children, tx.Rebind(`SELECT COUNT(*) FROM dictionary_items WHERE parent_id=? AND deleted_at IS NULL`), id); e != nil {
			return e
		}
		if children > 0 {
			return ErrConflict
		}
		now := time.Now()
		result, e := tx.ExecContext(ctx, tx.Rebind(`UPDATE dictionary_items SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=? WHERE id=? AND version=? AND deleted_at IS NULL`), now, actor.ID, now, actor.ID, id, version)
		if e != nil {
			return e
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			return ErrConflict
		}
		_, e = tx.ExecContext(ctx, tx.Rebind(`UPDATE dictionary_definitions SET updated_at=?,updated_by=? WHERE id=?`), now, actor.ID, dictionaryID)
		if e != nil {
			return e
		}
		return s.recordTx(ctx, tx, "dictionary.item.delete", "dictionary_item", id, "", map[string]any{"version": version}, started)
	})
	if err != nil {
		s.recordFailure(ctx, "dictionary.item.delete", "dictionary_item", id, "", map[string]any{"version": version}, started)
	}
	return err
}
func validateQuery(q Query) error {
	if !codePattern.MatchString(strings.ToLower(strings.TrimSpace(q.Code))) || len(q.Keyword) > 256 || len(q.IDs) > 200 || len(q.Codes) > 200 || !validJSON(q.Extension) {
		return ErrInvalid
	}
	for _, id := range q.IDs {
		if uuid.Validate(id) != nil {
			return ErrInvalid
		}
	}
	for _, code := range q.Codes {
		if !codePattern.MatchString(strings.ToLower(strings.TrimSpace(code))) {
			return ErrInvalid
		}
	}
	for _, v := range q.Sort {
		if (v.Field != "sort_order" && v.Field != "code" && v.Field != "name") || (v.Direction != "asc" && v.Direction != "desc") {
			return ErrInvalid
		}
	}
	return nil
}
func (s *Service) Query(ctx context.Context, q Query) (Result, error) {
	q.Code = strings.ToLower(strings.TrimSpace(q.Code))
	q.Keyword = strings.TrimSpace(q.Keyword)
	for i := range q.Codes {
		q.Codes[i] = strings.ToLower(strings.TrimSpace(q.Codes[i]))
	}
	q.Extension = normalizeJSON(q.Extension)
	if err := validateQuery(q); err != nil {
		return Result{}, err
	}
	var d Definition
	err := s.db.GetContext(ctx, &d, s.db.Rebind(`SELECT `+definitionColumns+` FROM dictionary_definitions WHERE lower(dictionary_code)=? AND status='active' AND deleted_at IS NULL`), q.Code)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrNotFound
	}
	if err != nil {
		return Result{}, err
	}
	if d.Kind == KindTree && (q.Page != 0 || q.PageSize != 0) {
		return Result{}, ErrInvalid
	}
	requestJSON, _ := json.Marshal(q)
	cacheKey := fmt.Sprintf("dictionary:query:%s:%d:%x", d.ID, d.Version, sha256.Sum256(requestJSON))
	if s.cache != nil {
		if cached, cacheErr := cache.GetJSON[Result](ctx, s.cache, cacheKey); cacheErr == nil {
			return cached, nil
		}
	}
	var result Result
	if d.Source == SourceProvider {
		if s.providers == nil {
			return Result{}, ErrProviderUnavailable
		}
		result, err = s.providers.Query(ctx, q)
		if err != nil {
			err = fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
		}
		if err == nil && (result.Code != d.Code || result.Type != d.Kind || !validProviderResult(result, q, s.cfg.Dictionary.MaxTreeNodes)) {
			err = ErrProviderUnavailable
		}
	} else {
		result, err = s.queryStatic(ctx, d, q)
	}
	if err == nil && s.cache != nil {
		_ = cache.SetJSON(ctx, s.cache, cacheKey, result, s.cfg.Dictionary.CacheTTL)
	}
	return result, err
}
func validProviderResult(result Result, query Query, maxNodes int) bool {
	if result.Items == nil || len(result.Extension) == 0 || strings.TrimSpace(result.Name) == "" || len(result.Name) > 256 || !validJSON(result.Extension) {
		return false
	}
	count := 0
	seen := map[string]struct{}{}
	var walk func([]ResultItem, *string) bool
	walk = func(items []ResultItem, expectedParent *string) bool {
		for _, item := range items {
			count++
			if count > maxNodes || uuid.Validate(item.ID) != nil || !codePattern.MatchString(item.Code) || strings.TrimSpace(item.Name) == "" || len(item.Name) > 256 || len(item.Value) > maxValueLength || (!query.IncludeDisabled && item.Disabled) {
				return false
			}
			if (expectedParent == nil) != (item.ParentID == nil) || (expectedParent != nil && *expectedParent != *item.ParentID) {
				return false
			}
			if _, exists := seen[item.ID]; exists {
				return false
			}
			seen[item.ID] = struct{}{}
			if result.Type == KindEnum && len(item.Children) != 0 {
				return false
			}
			if len(item.Extension) == 0 || !validJSON(item.Extension) || !walk(item.Children, &item.ID) {
				return false
			}
		}
		return true
	}
	if result.Type == KindEnum {
		page, err := normalizePage(query.Page, query.PageSize)
		if err != nil || result.Page != page[0] || result.PageSize != page[1] || len(result.Items) > page[1] || result.Total < int64(len(result.Items)) {
			return false
		}
	} else if result.Page != 0 || result.PageSize != 0 {
		return false
	}
	if !walk(result.Items, nil) {
		return false
	}
	if result.Type == KindTree && result.Total != int64(count) {
		return false
	}
	return true
}
func (s *Service) queryStatic(ctx context.Context, d Definition, q Query) (Result, error) {
	args := []any{d.ID}
	where := `dictionary_id=? AND deleted_at IS NULL`
	order := `sort_order ASC,code ASC`
	if len(q.Sort) > 0 {
		parts := make([]string, 0, len(q.Sort))
		for _, v := range q.Sort {
			parts = append(parts, v.Field+" "+strings.ToUpper(v.Direction))
		}
		order = strings.Join(parts, ",")
	}
	if d.Kind == KindEnum {
		if !q.IncludeDisabled {
			where += ` AND disabled=false`
		}
		if q.Keyword != "" {
			where += ` AND (lower(code) LIKE ? OR lower(name) LIKE ?)`
			like := "%" + strings.ToLower(q.Keyword) + "%"
			args = append(args, like, like)
		}
		where, args = appendIn(where, args, "id", q.IDs)
		where, args = appendIn(where, args, "code", q.Codes)
		n, err := normalizePage(q.Page, q.PageSize)
		if err != nil {
			return Result{}, err
		}
		var total int64
		if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT COUNT(*) FROM dictionary_items WHERE `+where), args...); err != nil {
			return Result{}, err
		}
		queryArgs := append(append([]any{}, args...), n[1], pagination.Offset(pagination.Request{Page: n[0], PageSize: n[1]}))
		items := []Item{}
		if err := s.db.SelectContext(ctx, &items, s.db.Rebind(`SELECT `+itemColumns+` FROM dictionary_items WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`), queryArgs...); err != nil {
			return Result{}, err
		}
		publicItems := make([]ResultItem, len(items))
		for i, item := range items {
			publicItems[i] = toResultItem(item)
		}
		return Result{Code: d.Code, Name: d.Name, Type: d.Kind, Items: publicItems, Page: n[0], PageSize: n[1], Total: total, Extension: d.Extension}, nil
	}
	items := []Item{}
	treeArgs := append(args, s.cfg.Dictionary.MaxTreeNodes+1)
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(`SELECT `+itemColumns+` FROM dictionary_items WHERE `+where+` ORDER BY `+order+` LIMIT ?`), treeArgs...); err != nil {
		return Result{}, err
	}
	if len(items) > s.cfg.Dictionary.MaxTreeNodes {
		return Result{}, ErrConflict
	}
	items = filterTreeItems(items, q)
	tree, err := buildResultTree(items, q.Sort)
	if err != nil {
		return Result{}, err
	}
	return Result{Code: d.Code, Name: d.Name, Type: d.Kind, Items: tree, Page: 0, PageSize: 0, Total: int64(len(items)), Extension: d.Extension}, nil
}
func appendIn(where string, args []any, column string, values []string) (string, []any) {
	if len(values) == 0 {
		return where, args
	}
	marks := make([]string, len(values))
	for i, v := range values {
		marks[i] = "?"
		args = append(args, v)
	}
	return where + ` AND ` + column + ` IN (` + strings.Join(marks, ",") + `)`, args
}
func normalizePage(page, size int) ([2]int, error) {
	if page == 0 {
		page = 1
	}
	if size == 0 {
		size = 20
	}
	if page < 1 || size < 1 || size > 200 {
		return [2]int{}, ErrInvalid
	}
	return [2]int{page, size}, nil
}
func filterTreeItems(items []Item, query Query) []Item {
	byID := make(map[string]Item, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	idFilter := make(map[string]struct{}, len(query.IDs))
	for _, id := range query.IDs {
		idFilter[id] = struct{}{}
	}
	codeFilter := make(map[string]struct{}, len(query.Codes))
	for _, code := range query.Codes {
		codeFilter[code] = struct{}{}
	}
	keep := map[string]struct{}{}
	for _, item := range items {
		if !query.IncludeDisabled && item.Disabled {
			continue
		}
		matched := true
		if query.Keyword != "" && !strings.Contains(strings.ToLower(item.Code), strings.ToLower(query.Keyword)) && !strings.Contains(strings.ToLower(item.Name), strings.ToLower(query.Keyword)) {
			matched = false
		}
		if _, ok := idFilter[item.ID]; len(idFilter) != 0 && !ok {
			matched = false
		}
		if _, ok := codeFilter[item.Code]; len(codeFilter) != 0 && !ok {
			matched = false
		}
		if !matched {
			continue
		}
		chain := []string{}
		current := item
		valid := true
		seen := map[string]struct{}{}
		for {
			if _, exists := seen[current.ID]; exists {
				valid = false
				break
			}
			seen[current.ID] = struct{}{}
			chain = append(chain, current.ID)
			if current.ParentID == nil {
				break
			}
			parent, exists := byID[*current.ParentID]
			if !exists || (!query.IncludeDisabled && parent.Disabled) {
				valid = false
				break
			}
			current = parent
		}
		if valid {
			for _, id := range chain {
				keep[id] = struct{}{}
			}
		}
	}
	filtered := make([]Item, 0, len(keep))
	for _, item := range items {
		if _, ok := keep[item.ID]; ok {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func buildTree(items []Item, rules []Sort) ([]Item, error) {
	byParent := map[string][]Item{}
	ids := map[string]bool{}
	for _, v := range items {
		ids[v.ID] = true
	}
	for _, v := range items {
		key := ""
		if v.ParentID != nil && ids[*v.ParentID] {
			key = *v.ParentID
		}
		byParent[key] = append(byParent[key], v)
	}
	visited := map[string]bool{}
	visiting := map[string]bool{}
	var walk func(string) ([]Item, error)
	walk = func(id string) ([]Item, error) {
		children := append([]Item{}, byParent[id]...)
		sort.SliceStable(children, func(i, j int) bool { return itemLess(children[i], children[j], rules) })
		for i := range children {
			if visiting[children[i].ID] {
				return nil, ErrInvalid
			}
			visiting[children[i].ID] = true
			nested, err := walk(children[i].ID)
			if err != nil {
				return nil, err
			}
			children[i].Children = nested
			visiting[children[i].ID] = false
			visited[children[i].ID] = true
		}
		return children, nil
	}
	tree, err := walk("")
	if err != nil {
		return nil, err
	}
	if len(visited) != len(items) {
		return nil, ErrInvalid
	}
	return tree, nil
}
func itemLess(left, right Item, rules []Sort) bool {
	if len(rules) == 0 {
		rules = []Sort{{Field: "sort_order", Direction: "asc"}, {Field: "code", Direction: "asc"}}
	}
	for _, rule := range rules {
		comparison := 0
		switch rule.Field {
		case "sort_order":
			if left.SortOrder < right.SortOrder {
				comparison = -1
			} else if left.SortOrder > right.SortOrder {
				comparison = 1
			}
		case "code":
			comparison = strings.Compare(left.Code, right.Code)
		case "name":
			comparison = strings.Compare(left.Name, right.Name)
		}
		if comparison != 0 {
			if rule.Direction == "desc" {
				return comparison > 0
			}
			return comparison < 0
		}
	}
	return left.ID < right.ID
}
func toResultItem(item Item) ResultItem {
	return ResultItem{ID: item.ID, ParentID: item.ParentID, Code: item.Code, Name: item.Name, Value: item.Value, Disabled: item.Disabled, SortOrder: item.SortOrder, Extension: item.Extension}
}
func buildResultTree(items []Item, rules []Sort) ([]ResultItem, error) {
	tree, err := buildTree(items, rules)
	if err != nil {
		return nil, err
	}
	var convert func([]Item) []ResultItem
	convert = func(nodes []Item) []ResultItem {
		result := make([]ResultItem, len(nodes))
		for i, node := range nodes {
			result[i] = toResultItem(node)
			result[i].Children = convert(node.Children)
		}
		return result
	}
	return convert(tree), nil
}
