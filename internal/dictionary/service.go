package dictionary

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
	platformtree "github.com/lihongjie0209/microservice-platform-go/tree"
)

var (
	ErrInvalid               = errors.New("invalid data dictionary")
	ErrNotFound              = errors.New("data dictionary not found")
	ErrConflict              = errors.New("data dictionary conflict")
	ErrResultTooLarge        = fmt.Errorf("%w: query result exceeds size limit", ErrConflict)
	ErrStaleVersion          = errors.New("data dictionary stale version")
	ErrProviderNotRegistered = errors.New("data dictionary provider not registered")
	ErrProviderUnavailable   = errors.New("data dictionary provider unavailable")
	codePattern              = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,127}$`)
)

const definitionColumns = `id,dictionary_code,name,dictionary_type,source_type,description,status,extension,created_at,created_by,updated_at,updated_by,version`
const itemColumns = `id,dictionary_id,parent_id,code,name,value,disabled,sort_order,extension,created_at,created_by,updated_at,updated_by,version`
const maxDescriptionLength = 4096
const maxValueLength = 1 << 20
const maxDictionaryTreeDepth = 512

// Limit serialized item data independently of node count: otherwise a tree
// with many individually valid values can exhaust memory during JSON encoding.
const maxDictionaryItemsBytes = 8 << 20

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

func storedDefinitionSize(d Definition) error {
	if len(d.Code) > 128 || len(d.Name) > 256 || len(d.Description) > maxDescriptionLength || len(d.Extension) > 16<<10 {
		return ErrResultTooLarge
	}
	return nil
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
	if in.Source == SourceProvider && !s.providers.Has(in.Code) {
		return Definition{}, ErrProviderNotRegistered
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
		err = storedDefinitionSize(d)
	}
	if err == nil {
		records := []Definition{d}
		err = s.presentDefinitions(ctx, records)
		d = records[0]
	}
	if err != nil {
		return Definition{}, err
	}
	return d, nil
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
	parentIDs := make([]string, 0, len(records))
	seenParents := make(map[string]struct{}, len(records))
	for _, record := range records {
		ids = append(ids, record.CreatedBy, record.UpdatedBy)
		if record.ParentID != nil {
			if _, seen := seenParents[*record.ParentID]; !seen {
				seenParents[*record.ParentID] = struct{}{}
				parentIDs = append(parentIDs, *record.ParentID)
			}
		}
	}
	names, err := presentation.ActorNames(ctx, s.actors, ids...)
	if err != nil {
		return err
	}
	type parentRecord struct {
		ID           string `db:"id"`
		DictionaryID string `db:"dictionary_id"`
		Name         string `db:"name"`
	}
	parents := make(map[string]parentRecord, len(parentIDs))
	if len(parentIDs) > 0 {
		query, args, err := sqlx.In(`SELECT id,dictionary_id,name FROM dictionary_items WHERE id IN (?) AND deleted_at IS NULL`, parentIDs)
		if err != nil {
			return fmt.Errorf("build dictionary parent lookup: %w", err)
		}
		var rows []parentRecord
		if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(query), args...); err != nil {
			return fmt.Errorf("load dictionary parent names: %w", err)
		}
		for _, parent := range rows {
			parents[parent.ID] = parent
		}
	}
	for index := range records {
		records[index].CreatedByName = names[records[index].CreatedBy]
		records[index].UpdatedByName = names[records[index].UpdatedBy]
		if records[index].ParentID != nil {
			if parent, ok := parents[*records[index].ParentID]; ok && parent.DictionaryID == records[index].DictionaryID {
				records[index].ParentName = parent.Name
			}
		}
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
		where += ` AND (lower(dictionary_code) LIKE ? ESCAPE '!' OR lower(name) LIKE ? ESCAPE '!')`
		like := database.LikeContainsPattern(strings.ToLower(in.Keyword))
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
	items, err := s.selectDefinitions(ctx, `SELECT `+definitionColumns+` FROM dictionary_definitions WHERE `+where+` ORDER BY dictionary_code LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return Page{}, err
	}
	if err := s.presentDefinitions(ctx, items); err != nil {
		return Page{}, err
	}
	return Page{Items: items, Page: page.Page, PageSize: page.PageSize, Total: total}, nil
}

func (s *Service) selectDefinitions(ctx context.Context, query string, args ...any) (items []Definition, err error) {
	rows, err := s.db.QueryxContext(ctx, s.db.Rebind(query), args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			items = nil
			err = errors.Join(err, fmt.Errorf("close dictionary definition rows: %w", closeErr))
		}
	}()
	items = make([]Definition, 0)
	for rows.Next() {
		var item Definition
		if err := rows.StructScan(&item); err != nil {
			return nil, err
		}
		if err := storedDefinitionSize(item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
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
		_, err = storedItemBytes(item)
	}
	if err == nil {
		records := []Item{item}
		err = s.presentItems(ctx, records)
		item = records[0]
	}
	if err != nil {
		return Item{}, err
	}
	return item, nil
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
	if in.ExcludeSubtreeID != "" && uuid.Validate(in.ExcludeSubtreeID) != nil {
		return ItemPage{}, ErrInvalid
	}
	prefix := ""
	prefixArgs := []any{}
	if in.ExcludeSubtreeID != "" {
		// UNION de-duplicates rows, so even a corrupted cycle cannot recurse forever.
		prefix = `WITH RECURSIVE excluded(id) AS (
			SELECT id FROM dictionary_items WHERE id=? AND dictionary_id=? AND deleted_at IS NULL
			UNION
			SELECT child.id FROM dictionary_items child JOIN excluded parent ON child.parent_id=parent.id
			WHERE child.dictionary_id=? AND child.deleted_at IS NULL
		) `
		prefixArgs = []any{in.ExcludeSubtreeID, in.DictionaryID, in.DictionaryID}
	}
	where := `dictionary_id=? AND deleted_at IS NULL`
	args := []any{in.DictionaryID}
	if in.Keyword != "" {
		where += ` AND (lower(code) LIKE ? ESCAPE '!' OR lower(name) LIKE ? ESCAPE '!')`
		like := database.LikeContainsPattern(strings.ToLower(in.Keyword))
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
	if in.ExcludeSubtreeID != "" {
		where += ` AND id NOT IN (SELECT id FROM excluded)`
	}
	var total int64
	countArgs := append(append([]any{}, prefixArgs...), args...)
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(prefix+`SELECT COUNT(*) FROM dictionary_items WHERE `+where), countArgs...); err != nil {
		return ItemPage{}, err
	}
	queryArgs := append(append(append([]any{}, prefixArgs...), args...), page.PageSize, pagination.Offset(page))
	items, err := s.selectQueryItems(ctx, prefix+`SELECT `+itemColumns+` FROM dictionary_items WHERE `+where+` ORDER BY sort_order,code,id LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
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

// The caller holds the dictionary definition row lock, serializing all item
// creates and value changes within this dictionary before checking the value.
func ensureSelectableValueAvailable(ctx context.Context, tx *sqlx.Tx, dictionaryID, itemID, value string) error {
	if value == "" {
		value = itemID
	}
	var conflictingID string
	err := tx.GetContext(ctx, &conflictingID, tx.Rebind(`SELECT id FROM dictionary_items WHERE dictionary_id=? AND id<>? AND deleted_at IS NULL AND COALESCE(NULLIF(value,''),id)=? LIMIT 1 FOR UPDATE`), dictionaryID, itemID, value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check dictionary selectable value: %w", err)
	}
	return ErrConflict
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
			if e := tx.GetContext(ctx, &count, tx.Rebind(`SELECT COUNT(*) FROM dictionary_items WHERE id=? AND dictionary_id=? AND deleted_at IS NULL`), *in.ParentID, in.DictionaryID); e != nil {
				return fmt.Errorf("check dictionary parent: %w", e)
			}
			if count != 1 {
				return ErrInvalid
			}
		}
		if e := ensureSelectableValueAvailable(ctx, tx, in.DictionaryID, id, in.Value); e != nil {
			return e
		}
		now := time.Now()
		_, e := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO dictionary_items(id,dictionary_id,parent_id,code,name,value,disabled,sort_order,extension,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,1)`), id, in.DictionaryID, in.ParentID, in.Code, in.Name, in.Value, in.Disabled, in.SortOrder, string(in.Extension), now, actor.ID, now, actor.ID)
		if e == nil && definition.Kind == KindTree {
			e = validateStoredTreeDepth(ctx, tx, in.DictionaryID)
		}
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
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			return ErrStaleVersion
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
			return ErrStaleVersion
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
		rows, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if rows == 0 {
			return ErrStaleVersion
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
		var candidateDictionaryID string
		if e := tx.GetContext(ctx, &candidateDictionaryID, tx.Rebind(`SELECT dictionary_id FROM dictionary_items WHERE id=? AND deleted_at IS NULL`), in.ID); e != nil {
			return e
		}
		var definition struct {
			Kind   Kind   `db:"dictionary_type"`
			Source Source `db:"source_type"`
		}
		if e := tx.GetContext(ctx, &definition, tx.Rebind(`SELECT dictionary_type,source_type FROM dictionary_definitions WHERE id=? AND deleted_at IS NULL FOR UPDATE`), candidateDictionaryID); e != nil {
			return e
		}
		if definition.Source != SourceStatic {
			return ErrInvalid
		}
		var currentItem struct {
			DictionaryID string `db:"dictionary_id"`
		}
		if e := tx.GetContext(ctx, &currentItem, tx.Rebind(`SELECT dictionary_id FROM dictionary_items WHERE id=? AND deleted_at IS NULL FOR UPDATE`), in.ID); e != nil {
			return e
		}
		if currentItem.DictionaryID != candidateDictionaryID {
			return ErrConflict
		}
		dictionaryID = currentItem.DictionaryID
		if definition.Kind == KindEnum && in.ParentID != nil {
			return ErrInvalid
		}
		if in.ParentID != nil {
			if e := validateItemParentChain(ctx, tx, dictionaryID, in.ID, *in.ParentID); e != nil {
				return e
			}
		}
		if e := ensureSelectableValueAvailable(ctx, tx, dictionaryID, in.ID, in.Value); e != nil {
			return e
		}
		now := time.Now()
		result, e := tx.ExecContext(ctx, tx.Rebind(`UPDATE dictionary_items SET parent_id=?,name=?,value=?,disabled=?,sort_order=?,extension=?,updated_at=?,updated_by=? WHERE id=? AND version=? AND deleted_at IS NULL`), in.ParentID, in.Name, in.Value, in.Disabled, in.SortOrder, string(in.Extension), now, actor.ID, in.ID, in.Version)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			return ErrStaleVersion
		}
		if definition.Kind == KindTree {
			if e := validateStoredTreeDepth(ctx, tx, dictionaryID); e != nil {
				return e
			}
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

func validateItemParentChain(ctx context.Context, tx *sqlx.Tx, dictionaryID, itemID, parentID string) error {
	var ancestors []struct {
		ID           string  `db:"id"`
		ParentID     *string `db:"parent_id"`
		DictionaryID string  `db:"dictionary_id"`
		Depth        int     `db:"depth"`
	}
	const query = `WITH RECURSIVE ancestors(id,parent_id,dictionary_id,depth) AS (
		SELECT id,parent_id,dictionary_id,1 FROM dictionary_items WHERE id=? AND deleted_at IS NULL
		UNION ALL
		SELECT parent.id,parent.parent_id,parent.dictionary_id,ancestors.depth+1
		FROM dictionary_items parent JOIN ancestors ON parent.id=ancestors.parent_id
		WHERE parent.deleted_at IS NULL AND ancestors.depth<?
	) SELECT id,parent_id,dictionary_id,depth FROM ancestors ORDER BY depth`
	if err := tx.SelectContext(ctx, &ancestors, tx.Rebind(query), parentID, maxDictionaryTreeDepth+1); err != nil {
		return fmt.Errorf("load dictionary ancestor: %w", err)
	}
	if len(ancestors) == 0 || len(ancestors) > maxDictionaryTreeDepth {
		return ErrInvalid
	}
	for index, ancestor := range ancestors {
		if ancestor.ID == itemID || ancestor.DictionaryID != dictionaryID || ancestor.Depth != index+1 {
			return ErrInvalid
		}
	}
	if ancestors[len(ancestors)-1].ParentID != nil {
		return ErrInvalid
	}
	return nil
}

func validateStoredTreeDepth(ctx context.Context, tx *sqlx.Tx, dictionaryID string) error {
	var counts struct {
		Total     int `db:"total"`
		Reachable int `db:"reachable"`
		MaxDepth  int `db:"max_depth"`
	}
	const query = `WITH RECURSIVE tree_nodes(id,depth) AS (
		SELECT id,1 FROM dictionary_items WHERE dictionary_id=? AND parent_id IS NULL AND deleted_at IS NULL
		UNION ALL
		SELECT child.id,tree_nodes.depth+1 FROM dictionary_items child JOIN tree_nodes ON child.parent_id=tree_nodes.id
		WHERE child.dictionary_id=? AND child.deleted_at IS NULL AND tree_nodes.depth<?
	) SELECT (SELECT COUNT(*) FROM dictionary_items WHERE dictionary_id=? AND deleted_at IS NULL) AS total,
		COUNT(*) AS reachable,COALESCE(MAX(depth),0) AS max_depth FROM tree_nodes`
	if err := tx.GetContext(ctx, &counts, tx.Rebind(query), dictionaryID, dictionaryID, maxDictionaryTreeDepth+1, dictionaryID); err != nil {
		return fmt.Errorf("validate dictionary tree depth: %w", err)
	}
	if counts.Reachable != counts.Total || counts.MaxDepth > maxDictionaryTreeDepth {
		return ErrInvalid
	}
	return nil
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
		if e := tx.GetContext(ctx, &dictionaryID, tx.Rebind(`SELECT dictionary_id FROM dictionary_items WHERE id=? AND deleted_at IS NULL`), id); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return ErrNotFound
			}
			return e
		}
		var lockedDefinitionID string
		if e := tx.GetContext(ctx, &lockedDefinitionID, tx.Rebind(`SELECT id FROM dictionary_definitions WHERE id=? AND deleted_at IS NULL FOR UPDATE`), dictionaryID); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return ErrNotFound
			}
			return e
		}
		var lockedDictionaryID string
		if e := tx.GetContext(ctx, &lockedDictionaryID, tx.Rebind(`SELECT dictionary_id FROM dictionary_items WHERE id=? AND deleted_at IS NULL FOR UPDATE`), id); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return ErrNotFound
			}
			return e
		}
		if lockedDictionaryID != dictionaryID {
			return ErrConflict
		}
		var childID string
		if e := tx.GetContext(ctx, &childID, tx.Rebind(`SELECT id FROM dictionary_items WHERE parent_id=? AND deleted_at IS NULL LIMIT 1 FOR UPDATE`), id); e == nil {
			return ErrConflict
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		now := time.Now()
		result, e := tx.ExecContext(ctx, tx.Rebind(`UPDATE dictionary_items SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=? WHERE id=? AND version=? AND deleted_at IS NULL`), now, actor.ID, now, actor.ID, id, version)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			return ErrStaleVersion
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
	if !codePattern.MatchString(strings.ToLower(strings.TrimSpace(q.Code))) || len(q.Keyword) > 256 || len(q.IDs) > 200 || len(q.Codes) > 200 || len(q.Values) > 200 || len(q.Sort) > 3 || !validJSON(q.Extension) {
		return ErrInvalid
	}
	if _, err := pagination.Normalize(pagination.Request{Page: q.Page, PageSize: q.PageSize}); err != nil {
		return ErrInvalid
	}
	valueBytes := 0
	for _, value := range q.Values {
		valueBytes += len(value)
		if strings.TrimSpace(value) == "" || valueBytes > 16*1024 {
			return ErrInvalid
		}
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
	seenSort := make(map[string]struct{}, len(q.Sort))
	for _, v := range q.Sort {
		if (v.Field != "sort_order" && v.Field != "code" && v.Field != "name") || (v.Direction != "asc" && v.Direction != "desc") {
			return ErrInvalid
		}
		if _, exists := seenSort[v.Field]; exists {
			return ErrInvalid
		}
		seenSort[v.Field] = struct{}{}
	}
	return nil
}
func (s *Service) Query(ctx context.Context, q Query) (Result, error) {
	q.Code = strings.ToLower(strings.TrimSpace(q.Code))
	q.Keyword = strings.TrimSpace(q.Keyword)
	q.Codes = append([]string(nil), q.Codes...)
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
	if err := storedDefinitionSize(d); err != nil {
		return Result{}, err
	}
	if d.Kind == KindTree && (q.Page != 0 || q.PageSize != 0) {
		return Result{}, ErrInvalid
	}
	maxResultNodes := s.cfg.Dictionary.MaxTreeNodes
	if d.Kind == KindEnum {
		maxResultNodes = pagination.MaxPageSize
	}
	cacheKey := ""
	if d.Source == SourceStatic && s.cache != nil {
		cacheQuery := canonicalStaticCacheQuery(q, d.Kind)
		requestJSON, _ := json.Marshal(cacheQuery)
		cacheKey = fmt.Sprintf("dictionary:query:v3:%s:%d:%x", d.ID, d.Version, sha256.Sum256(requestJSON))
		if cached, cacheErr := cache.GetJSON[Result](ctx, s.cache, cacheKey); cacheErr == nil && cached.Code == d.Code && cached.Type == d.Kind && validProviderResult(cached, q, maxResultNodes) {
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
		if err == nil && (result.Code != d.Code || result.Type != d.Kind || !validProviderResult(result, q, maxResultNodes)) {
			err = ErrProviderUnavailable
		}
	} else {
		result, err = s.queryStatic(ctx, d, q)
	}
	if err == nil && cacheKey != "" {
		_ = cache.SetJSON(ctx, s.cache, cacheKey, result, s.cfg.Dictionary.CacheTTL)
	}
	return result, err
}

func canonicalStaticCacheQuery(query Query, kind Kind) Query {
	// Static dictionaries ignore provider-only extensions, and these filters
	// have set semantics. Canonicalize only the cache copy, not the SQL input.
	query.Extension = nil
	query.Keyword = strings.ToLower(query.Keyword)
	query.IDs = canonicalFilterSet(query.IDs)
	query.Codes = canonicalFilterSet(query.Codes)
	query.Values = canonicalFilterSet(query.Values)
	if len(query.Sort) == 0 {
		query.Sort = nil
	}
	if kind == KindEnum {
		if query.Page == 0 {
			query.Page = 1
		}
		if query.PageSize == 0 {
			query.PageSize = pagination.DefaultPageSize
		}
	}
	return query
}

func canonicalFilterSet(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	ordered := slices.Clone(values)
	slices.Sort(ordered)
	return slices.Compact(ordered)
}

func validProviderResult(result Result, query Query, maxNodes int) bool {
	if result.Items == nil || len(result.Extension) == 0 || strings.TrimSpace(result.Name) == "" || len(result.Name) > 256 || !validJSON(result.Extension) {
		return false
	}
	count := 0
	itemBytes := 0
	seen := map[string]struct{}{}
	seenCodes := map[string]struct{}{}
	seenValues := map[string]struct{}{}
	allowedIDs := make(map[string]struct{}, len(query.IDs))
	for _, id := range query.IDs {
		allowedIDs[id] = struct{}{}
	}
	allowedCodes := make(map[string]struct{}, len(query.Codes))
	for _, code := range query.Codes {
		allowedCodes[code] = struct{}{}
	}
	allowedValues := make(map[string]struct{}, len(query.Values))
	for _, value := range query.Values {
		allowedValues[value] = struct{}{}
	}
	keyword := strings.ToLower(query.Keyword)
	matches := func(item ResultItem, value string) bool {
		if keyword != "" && !strings.Contains(strings.ToLower(item.Code), keyword) && !strings.Contains(strings.ToLower(item.Name), keyword) {
			return false
		}
		if _, ok := allowedIDs[item.ID]; len(allowedIDs) != 0 && !ok {
			return false
		}
		if _, ok := allowedCodes[item.Code]; len(allowedCodes) != 0 && !ok {
			return false
		}
		if _, ok := allowedValues[value]; len(allowedValues) != 0 && !ok {
			return false
		}
		return true
	}
	var walk func([]ResultItem, *string, int) (bool, bool)
	walk = func(items []ResultItem, expectedParent *string, depth int) (bool, bool) {
		if len(items) == 0 {
			return false, true
		}
		if depth > maxDictionaryTreeDepth {
			return false, false
		}
		anyMatch := false
		for index, item := range items {
			count++
			if count > maxNodes || uuid.Validate(item.ID) != nil || !codePattern.MatchString(item.Code) || strings.TrimSpace(item.Name) == "" || len(item.Name) > 256 || len(item.Value) > maxValueLength || len(item.Extension) == 0 || !validJSON(item.Extension) || (!query.IncludeDisabled && item.Disabled) {
				return false, false
			}
			encodedBytes, err := resultItemBytes(item)
			if err != nil || encodedBytes > maxDictionaryItemsBytes-itemBytes {
				return false, false
			}
			itemBytes += encodedBytes
			if (expectedParent == nil) != (item.ParentID == nil) || (expectedParent != nil && *expectedParent != *item.ParentID) {
				return false, false
			}
			if index > 0 && resultItemLess(item, items[index-1], query.Sort) {
				return false, false
			}
			if _, exists := seen[item.ID]; exists {
				return false, false
			}
			seen[item.ID] = struct{}{}
			if _, exists := seenCodes[item.Code]; exists {
				return false, false
			}
			seenCodes[item.Code] = struct{}{}
			selectableValue := item.Value
			if selectableValue == "" {
				selectableValue = item.ID
			}
			if _, exists := seenValues[selectableValue]; exists {
				return false, false
			}
			seenValues[selectableValue] = struct{}{}
			if result.Type == KindEnum && len(item.Children) != 0 {
				return false, false
			}
			childMatch, valid := walk(item.Children, &item.ID, depth+1)
			if !valid || (!matches(item, selectableValue) && !childMatch) {
				return false, false
			}
			anyMatch = true
		}
		return anyMatch, true
	}
	if result.Type == KindEnum {
		page, err := normalizePage(query.Page, query.PageSize)
		if err != nil || result.Page != page[0] || result.PageSize != page[1] || len(result.Items) > page[1] || result.Total < 0 {
			return false
		}
	} else if result.Page != 0 || result.PageSize != 0 {
		return false
	}
	if _, valid := walk(result.Items, nil, 1); !valid {
		return false
	}
	if result.Type == KindTree && result.Total != int64(count) {
		return false
	}
	return true
}

func resultItemBytes(item ResultItem) (int, error) {
	// A shallow encoding accounts for all fields except descendants, which the
	// caller visits separately. It also accounts for JSON escaping of values.
	item.Children = nil
	encoded, err := json.Marshal(item)
	if err != nil {
		return 0, err
	}
	return len(encoded), nil
}

func storedItemBytes(item Item) (int, error) {
	// Migrations or maintenance can write rows without the API's input checks.
	// Reject oversized fields before JSON escaping amplifies their size.
	if len(item.Code) > 128 || len(item.Name) > 256 || len(item.Value) > maxValueLength || len(item.Extension) > 16<<10 {
		return 0, ErrResultTooLarge
	}
	encodedBytes, err := resultItemBytes(toResultItem(item))
	if err != nil {
		return 0, fmt.Errorf("encode dictionary item size: %w", err)
	}
	if encodedBytes > maxDictionaryItemsBytes {
		return 0, ErrResultTooLarge
	}
	return encodedBytes, nil
}

func (s *Service) selectQueryItems(ctx context.Context, query string, args ...any) (items []Item, err error) {
	rows, err := s.db.QueryxContext(ctx, s.db.Rebind(query), args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			items = nil
			err = errors.Join(err, fmt.Errorf("close dictionary item rows: %w", closeErr))
		}
	}()
	items = make([]Item, 0)
	itemBytes := 0
	for rows.Next() {
		var item Item
		if err := rows.StructScan(&item); err != nil {
			return nil, err
		}
		encodedBytes, err := storedItemBytes(item)
		if err != nil {
			return nil, err
		}
		if encodedBytes > maxDictionaryItemsBytes-itemBytes {
			return nil, ErrResultTooLarge
		}
		itemBytes += encodedBytes
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
func resultItemLess(left, right ResultItem, rules []Sort) bool {
	return itemLess(
		Item{ID: left.ID, Code: left.Code, Name: left.Name, SortOrder: left.SortOrder},
		Item{ID: right.ID, Code: right.Code, Name: right.Name, SortOrder: right.SortOrder},
		rules,
	)
}
func (s *Service) queryStatic(ctx context.Context, d Definition, q Query) (Result, error) {
	args := []any{d.ID}
	where := `dictionary_id=? AND deleted_at IS NULL`
	order := `sort_order ASC,code ASC,id ASC`
	if len(q.Sort) > 0 {
		parts := make([]string, 0, len(q.Sort))
		for _, v := range q.Sort {
			parts = append(parts, v.Field+" "+strings.ToUpper(v.Direction))
		}
		order = strings.Join(parts, ",") + `,id ASC`
	}
	if !q.IncludeDisabled {
		where += ` AND disabled=false`
	}
	if d.Kind == KindEnum {
		if q.Keyword != "" {
			where += ` AND (lower(code) LIKE ? ESCAPE '!' OR lower(name) LIKE ? ESCAPE '!')`
			like := database.LikeContainsPattern(strings.ToLower(q.Keyword))
			args = append(args, like, like)
		}
		where, args = appendIn(where, args, "id", q.IDs)
		where, args = appendIn(where, args, "code", q.Codes)
		where, args = appendIn(where, args, "COALESCE(NULLIF(value,''),id)", q.Values)
		n, err := normalizePage(q.Page, q.PageSize)
		if err != nil {
			return Result{}, err
		}
		var total int64
		if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT COUNT(*) FROM dictionary_items WHERE `+where), args...); err != nil {
			return Result{}, err
		}
		queryArgs := append(append([]any{}, args...), n[1], pagination.Offset(pagination.Request{Page: n[0], PageSize: n[1]}))
		items, err := s.selectQueryItems(ctx, `SELECT `+itemColumns+` FROM dictionary_items WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, queryArgs...)
		if err != nil {
			return Result{}, err
		}
		publicItems := make([]ResultItem, len(items))
		for i, item := range items {
			publicItems[i] = toResultItem(item)
		}
		return Result{Code: d.Code, Name: d.Name, Type: d.Kind, Items: publicItems, Page: n[0], PageSize: n[1], Total: total, Extension: d.Extension}, nil
	}
	treeArgs := append(args, s.cfg.Dictionary.MaxTreeNodes+1)
	items, err := s.selectQueryItems(ctx, `SELECT `+itemColumns+` FROM dictionary_items WHERE `+where+` ORDER BY `+order+` LIMIT ?`, treeArgs...)
	if err != nil {
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
	normalized, err := pagination.Normalize(pagination.Request{Page: page, PageSize: size})
	if err != nil {
		return [2]int{}, ErrInvalid
	}
	return [2]int{normalized.Page, normalized.PageSize}, nil
}
func filterTreeItems(items []Item, query Query) []Item {
	byID := make(map[string]Item, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	keyword := strings.ToLower(query.Keyword)
	idFilter := make(map[string]struct{}, len(query.IDs))
	for _, id := range query.IDs {
		idFilter[id] = struct{}{}
	}
	codeFilter := make(map[string]struct{}, len(query.Codes))
	for _, code := range query.Codes {
		codeFilter[code] = struct{}{}
	}
	valueFilter := make(map[string]struct{}, len(query.Values))
	for _, value := range query.Values {
		valueFilter[value] = struct{}{}
	}
	const (
		checking uint8 = iota + 1
		validPath
		invalidPath
	)
	// Resolve each matched node's ancestry once. Rewalking every matching
	// node's full parent chain is quadratic for a deep tree.
	pathStatus := make(map[string]uint8, len(items))
	path := make([]string, 0, 8)
	for _, item := range items {
		if !query.IncludeDisabled && item.Disabled {
			continue
		}
		matched := true
		if keyword != "" && !strings.Contains(strings.ToLower(item.Code), keyword) && !strings.Contains(strings.ToLower(item.Name), keyword) {
			matched = false
		}
		if _, ok := idFilter[item.ID]; len(idFilter) != 0 && !ok {
			matched = false
		}
		if _, ok := codeFilter[item.Code]; len(codeFilter) != 0 && !ok {
			matched = false
		}
		selectedValue := item.Value
		if selectedValue == "" {
			selectedValue = item.ID
		}
		if _, ok := valueFilter[selectedValue]; len(valueFilter) != 0 && !ok {
			matched = false
		}
		if !matched {
			continue
		}
		path = path[:0]
		current := item
		valid := true
	resolve:
		for {
			switch pathStatus[current.ID] {
			case checking, invalidPath:
				valid = false
				break resolve
			case validPath:
				break resolve
			}
			pathStatus[current.ID] = checking
			path = append(path, current.ID)
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
		status := invalidPath
		if valid {
			status = validPath
		}
		for _, id := range path {
			pathStatus[id] = status
		}
	}
	filtered := make([]Item, 0, len(pathStatus))
	for _, item := range items {
		if pathStatus[item.ID] == validPath {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func buildItemForest(items []Item, rules []Sort) ([]*platformtree.Node[Item], error) {
	ordered := append([]Item(nil), items...)
	sort.SliceStable(ordered, func(i, j int) bool { return itemLess(ordered[i], ordered[j], rules) })
	allRoots := true
	for _, item := range ordered {
		if item.ID == "" {
			return nil, ErrInvalid
		}
		if item.ParentID != nil {
			allRoots = false
			break
		}
	}
	buildWhole := func() ([]*platformtree.Node[Item], error) {
		forest, err := platformtree.Build(ordered, func(v Item) string { return v.ID }, func(v Item) (string, bool) {
			if v.ParentID == nil {
				return "", false
			}
			return *v.ParentID, true
		})
		if err != nil {
			return nil, ErrInvalid
		}
		return forest, nil
	}
	if allRoots {
		return buildWhole()
	}
	parents := make(map[string]*string, len(ordered))
	for _, item := range ordered {
		if item.ID == "" {
			return nil, ErrInvalid
		}
		if _, exists := parents[item.ID]; exists {
			return nil, ErrInvalid
		}
		parents[item.ID] = item.ParentID
	}
	depths := make(map[string]int, len(ordered))
	state := make(map[string]byte, len(ordered))
	maxDepth := 0
	for _, item := range ordered {
		if state[item.ID] == 2 {
			continue
		}
		path := make([]string, 0)
		current := item.ID
		base := 0
		for {
			switch state[current] {
			case 1:
				return nil, ErrInvalid
			case 2:
				base = depths[current]
				goto resolved
			}
			state[current] = 1
			path = append(path, current)
			parent := parents[current]
			if parent == nil {
				goto resolved
			}
			if _, exists := parents[*parent]; !exists {
				return nil, ErrInvalid
			}
			current = *parent
		}
	resolved:
		for i := len(path) - 1; i >= 0; i-- {
			base++
			depths[path[i]] = base
			state[path[i]] = 2
			if base > maxDepth {
				maxDepth = base
			}
		}
	}
	// The SDK validates each item's entire ancestor chain. Small windows keep
	// validation bounded while preserving its ordering and structural checks.
	const maxSDKTreeDepth = 32
	if maxDepth > maxDictionaryTreeDepth {
		return nil, ErrInvalid
	}
	if maxDepth <= maxSDKTreeDepth {
		return buildWhole()
	}
	groups := make([][]Item, (maxDepth+maxSDKTreeDepth-1)/maxSDKTreeDepth)
	for _, item := range ordered {
		group := (depths[item.ID] - 1) / maxSDKTreeDepth
		groups[group] = append(groups[group], item)
	}
	forest := make([]*platformtree.Node[Item], 0)
	nodes := make(map[string]*platformtree.Node[Item], len(ordered))
	for groupIndex, group := range groups {
		partial, err := platformtree.Build(group, func(v Item) string { return v.ID }, func(v Item) (string, bool) {
			if v.ParentID == nil || (depths[*v.ParentID]-1)/maxSDKTreeDepth != groupIndex {
				return "", false
			}
			return *v.ParentID, true
		})
		if err != nil {
			return nil, ErrInvalid
		}
		if groupIndex == 0 {
			forest = partial
		} else {
			for _, root := range partial {
				if root.Value.ParentID == nil {
					return nil, ErrInvalid
				}
				parent, exists := nodes[*root.Value.ParentID]
				if !exists {
					return nil, ErrInvalid
				}
				parent.Children = append(parent.Children, root)
			}
		}
		stack := append([]*platformtree.Node[Item](nil), partial...)
		for len(stack) > 0 {
			last := len(stack) - 1
			node := stack[last]
			stack = stack[:last]
			nodes[node.Value.ID] = node
			stack = append(stack, node.Children...)
		}
	}
	return forest, nil
}

func buildTree(items []Item, rules []Sort) ([]Item, error) {
	forest, err := buildItemForest(items, rules)
	if err != nil {
		return nil, err
	}
	result := make([]Item, len(forest))
	type pending struct {
		source *platformtree.Node[Item]
		target *Item
	}
	stack := make([]pending, 0, len(items))
	for i, node := range forest {
		stack = append(stack, pending{source: node, target: &result[i]})
	}
	for len(stack) > 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		*current.target = current.source.Value
		current.target.Children = make([]Item, len(current.source.Children))
		for i, child := range current.source.Children {
			stack = append(stack, pending{source: child, target: &current.target.Children[i]})
		}
	}
	return result, nil
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
	forest, err := buildItemForest(items, rules)
	if err != nil {
		return nil, err
	}
	result := make([]ResultItem, len(forest))
	type pending struct {
		source *platformtree.Node[Item]
		target *ResultItem
	}
	stack := make([]pending, 0, len(items))
	for i, node := range forest {
		stack = append(stack, pending{source: node, target: &result[i]})
	}
	for len(stack) > 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		*current.target = toResultItem(current.source.Value)
		current.target.Children = make([]ResultItem, len(current.source.Children))
		for i, child := range current.source.Children {
			stack = append(stack, pending{source: child, target: &current.target.Children[i]})
		}
	}
	return result, nil
}
