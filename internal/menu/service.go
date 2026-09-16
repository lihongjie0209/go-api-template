package menu

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/authorization"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/lihongjie0209/microservice-platform-go/stableid"
	platformtree "github.com/lihongjie0209/microservice-platform-go/tree"
)

var (
	ErrInvalid  = errors.New("invalid menu")
	ErrNotFound = errors.New("menu not found")
	ErrConflict = errors.New("menu conflict")
)
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{1,127}$`)

var (
	routePathPattern = regexp.MustCompile(`^/[A-Za-z0-9_./:-]*$`)
	componentPattern = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
)

const menuNamespace = "3cab5b18-f613-4b50-9c7c-4465fcbafe95"

const (
	menuCacheAll           = "menus:v1:tree-source:all"
	menuCacheVisible       = "menus:v1:tree-source:visible"
	maxMenuIDLength        = 128
	maxMenuNameLength      = 256
	maxMenuPathLength      = 2048
	maxMenuComponentLength = 512
	maxMenuIconLength      = 256
	maxMenuKeywordLength   = 256
	maxMenuSortOrder       = 1_000_000_000
	maxMenuMetadataBytes   = 1 << 20
)

type Record struct {
	ID            string          `db:"id" json:"id"`
	ParentID      *string         `db:"parent_id" json:"parent_id"`
	Key           string          `db:"menu_key" json:"menu_key"`
	Name          string          `db:"name" json:"name"`
	Type          string          `db:"menu_type" json:"menu_type"`
	RoutePath     string          `db:"route_path" json:"route_path"`
	Component     string          `db:"component" json:"component"`
	ExternalURL   string          `db:"external_url" json:"external_url"`
	Icon          string          `db:"icon" json:"icon"`
	PermissionID  *string         `db:"permission_id" json:"permission_id"`
	Visible       bool            `db:"visible" json:"visible"`
	Status        string          `db:"status" json:"status"`
	SortOrder     int64           `db:"sort_order" json:"sort_order"`
	Metadata      json.RawMessage `db:"metadata" json:"metadata" swaggertype:"object"`
	CreatedAt     time.Time       `db:"created_at" json:"created_at"`
	CreatedBy     string          `db:"created_by" json:"created_by"`
	CreatedByName string          `db:"-" json:"created_by_name"`
	UpdatedAt     time.Time       `db:"updated_at" json:"updated_at"`
	UpdatedBy     string          `db:"updated_by" json:"updated_by"`
	UpdatedByName string          `db:"-" json:"updated_by_name"`
	Version       int64           `db:"version" json:"version"`
}
type Node struct {
	Record
	Children []*Node `json:"children"`
}
type Input struct {
	ParentID                                                         *string
	Key, Name, Type, RoutePath, Component, ExternalURL, Icon, Status string
	PermissionID                                                     *string
	Visible                                                          bool
	SortOrder                                                        int64
	Metadata                                                         json.RawMessage
}
type UpdateInput struct {
	ID                                                          string
	ParentID                                                    *string
	Name, Type, RoutePath, Component, ExternalURL, Icon, Status string
	PermissionID                                                *string
	Visible                                                     bool
	SortOrder, Version                                          int64
	Metadata                                                    json.RawMessage
}
type TreeInput struct {
	Keyword       string
	IDs           []string
	Types         []string
	Statuses      []string
	CreatedAtFrom *time.Time
	CreatedAtTo   *time.Time
}
type Service struct {
	db            *sqlx.DB
	tx            *database.Transactor
	locker        cache.Locker
	cache         cache.Store
	operations    operationlog.TransactionalRecorder
	security      securitylog.TransactionalRecorder
	actors        presentation.ActorResolver
	authorization *authorization.TenantAuthorizationService
	logger        *slog.Logger
	cfg           config.Config
}

func New(db *sqlx.DB, tx *database.Transactor, locker cache.Locker, store cache.Store, operations operationlog.TransactionalRecorder, security securitylog.TransactionalRecorder, actors presentation.ActorResolver, authorizationService *authorization.TenantAuthorizationService, logger *slog.Logger, cfg config.Config) *Service {
	return &Service{db: db, tx: tx, locker: locker, cache: store, operations: operations, security: security, actors: actors, authorization: authorizationService, logger: logger, cfg: cfg}
}

const columns = `id,parent_id,menu_key,name,menu_type,route_path,component,external_url,icon,permission_id,visible,status,sort_order,metadata,created_at,created_by,updated_at,updated_by,version`

func validate(input Input) error {
	if !keyPattern.MatchString(input.Key) || input.Name == "" || len(input.Name) > maxMenuNameLength ||
		len(input.RoutePath) > maxMenuPathLength || len(input.Component) > maxMenuComponentLength ||
		len(input.ExternalURL) > maxMenuPathLength || len(input.Icon) > maxMenuIconLength ||
		input.SortOrder < -maxMenuSortOrder || input.SortOrder > maxMenuSortOrder ||
		!validMenuID(input.ParentID) || !validMenuID(input.PermissionID) ||
		(input.Status != "active" && input.Status != "disabled") || len(input.Metadata) > maxMenuMetadataBytes || !validMetadata(input.Metadata) {
		return ErrInvalid
	}
	switch input.Type {
	case "directory":
		if input.Component != "" || input.ExternalURL != "" || (input.RoutePath != "" && !validRoutePath(input.RoutePath)) {
			return ErrInvalid
		}
	case "page":
		if !validRoutePath(input.RoutePath) || !validComponent(input.Component) || input.ExternalURL != "" {
			return ErrInvalid
		}
	case "button":
		if input.ParentID == nil || input.PermissionID == nil || input.RoutePath != "" || input.Component != "" || input.ExternalURL != "" {
			return ErrInvalid
		}
	case "external":
		parsed, err := url.ParseRequestURI(input.ExternalURL)
		if input.RoutePath != "" || input.Component != "" || err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func (s *Service) Create(ctx context.Context, input Input) (Record, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Record{}, err
	}
	input = normalizeInput(input)
	if len(input.Metadata) == 0 {
		input.Metadata = json.RawMessage(`{}`)
	}
	if err := validate(input); err != nil {
		return Record{}, err
	}
	generator, err := stableid.New(menuNamespace)
	if err != nil {
		return Record{}, fmt.Errorf("configure menu stable ID: %w", err)
	}
	id, err := generator.String(input.Key)
	if err != nil {
		return Record{}, fmt.Errorf("generate menu stable ID: %w", err)
	}
	err = s.mutate(ctx, "platform.menu.create", id, input, func(lockCtx context.Context, tx *sqlx.Tx) error {
		if err := s.validateReferences(lockCtx, tx, "", input.Type, input.ParentID, input.PermissionID); err != nil {
			return err
		}
		now := time.Now()
		var deletedAt sql.NullTime
		findErr := tx.GetContext(lockCtx, &deletedAt, tx.Rebind(`SELECT deleted_at FROM menus WHERE id=? FOR UPDATE`), id)
		switch {
		case findErr == nil && !deletedAt.Valid:
			return ErrConflict
		case findErr == nil:
			_, err := tx.ExecContext(lockCtx, tx.Rebind(`UPDATE menus SET parent_id=?,menu_key=?,name=?,menu_type=?,route_path=?,component=?,external_url=?,icon=?,permission_id=?,visible=?,status=?,sort_order=?,metadata=?,deleted_at=NULL,deleted_by=NULL,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND deleted_at IS NOT NULL`), input.ParentID, input.Key, input.Name, input.Type, input.RoutePath, input.Component, input.ExternalURL, input.Icon, input.PermissionID, input.Visible, input.Status, input.SortOrder, string(input.Metadata), now, actor.ID, id)
			return err
		case errors.Is(findErr, sql.ErrNoRows):
			_, err := tx.ExecContext(lockCtx, tx.Rebind(`INSERT INTO menus(id,parent_id,menu_key,name,menu_type,route_path,component,external_url,icon,permission_id,visible,status,sort_order,metadata,created_at,created_by,updated_at,updated_by,version)VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1)`), id, input.ParentID, input.Key, input.Name, input.Type, input.RoutePath, input.Component, input.ExternalURL, input.Icon, input.PermissionID, input.Visible, input.Status, input.SortOrder, string(input.Metadata), now, actor.ID, now, actor.ID)
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
	if id == "" || len(id) > maxMenuIDLength {
		return Record{}, ErrInvalid
	}
	var record Record
	err := s.db.GetContext(ctx, &record, s.db.Rebind(`SELECT `+columns+` FROM menus WHERE id=? AND deleted_at IS NULL`), id)
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
func (s *Service) Tree(ctx context.Context, input TreeInput) ([]*Node, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return nil, err
	}
	if err := validateTreeInput(input); err != nil {
		return nil, err
	}
	records, err := s.list(ctx, false)
	if err != nil {
		return nil, err
	}
	records = filterRecords(records, input)
	if err := s.present(ctx, records); err != nil {
		return nil, err
	}
	return build(records)
}

func validateTreeInput(input TreeInput) error {
	if len(strings.TrimSpace(input.Keyword)) > maxMenuKeywordLength || len(input.IDs) > 200 || len(input.Types) > 10 || len(input.Statuses) > 10 || (input.CreatedAtFrom != nil && input.CreatedAtTo != nil && !input.CreatedAtFrom.Before(*input.CreatedAtTo)) {
		return ErrInvalid
	}
	for _, id := range input.IDs {
		if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) || len(id) > maxMenuIDLength {
			return ErrInvalid
		}
	}
	for _, menuType := range input.Types {
		if menuType != "directory" && menuType != "page" && menuType != "button" && menuType != "external" {
			return ErrInvalid
		}
	}
	for _, status := range input.Statuses {
		if status != "active" && status != "disabled" {
			return ErrInvalid
		}
	}
	return nil
}

func filterRecords(records []Record, input TreeInput) []Record {
	keyword := strings.ToLower(strings.TrimSpace(input.Keyword))
	if keyword == "" && len(input.IDs) == 0 && len(input.Types) == 0 && len(input.Statuses) == 0 && input.CreatedAtFrom == nil && input.CreatedAtTo == nil {
		return records
	}
	idSet, typeSet, statusSet := stringSet(input.IDs), stringSet(input.Types), stringSet(input.Statuses)
	byID := make(map[string]Record, len(records))
	matched := make(map[string]struct{})
	for _, record := range records {
		byID[record.ID] = record
		if len(idSet) > 0 && !contains(idSet, record.ID) || len(typeSet) > 0 && !contains(typeSet, record.Type) || len(statusSet) > 0 && !contains(statusSet, record.Status) {
			continue
		}
		if input.CreatedAtFrom != nil && record.CreatedAt.Before(*input.CreatedAtFrom) || input.CreatedAtTo != nil && !record.CreatedAt.Before(*input.CreatedAtTo) {
			continue
		}
		if keyword != "" && !strings.Contains(strings.ToLower(record.Key), keyword) && !strings.Contains(strings.ToLower(record.Name), keyword) && !strings.Contains(strings.ToLower(record.RoutePath), keyword) && !strings.Contains(strings.ToLower(record.Component), keyword) {
			continue
		}
		matched[record.ID] = struct{}{}
	}
	allowed := make(map[string]struct{}, len(matched))
	for id := range matched {
		visited := map[string]struct{}{}
		for id != "" {
			if _, seen := visited[id]; seen {
				break
			}
			visited[id] = struct{}{}
			record, ok := byID[id]
			if !ok {
				break
			}
			allowed[id] = struct{}{}
			if record.ParentID == nil {
				break
			}
			id = *record.ParentID
		}
	}
	result := make([]Record, 0, len(allowed))
	for _, record := range records {
		if contains(allowed, record.ID) {
			result = append(result, record)
		}
	}
	return result
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func contains(values map[string]struct{}, value string) bool {
	_, ok := values[value]
	return ok
}
func (s *Service) CurrentTree(ctx context.Context) ([]*Node, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return nil, err
	}
	permissionIDs, err := s.authorization.EffectivePermissions(ctx, "")
	if err != nil {
		return nil, err
	}
	records, err := s.list(ctx, true)
	if err != nil {
		return nil, err
	}
	permissions := map[string]struct{}{}
	for _, id := range permissionIDs {
		permissions[id] = struct{}{}
	}
	records = filterByPermissions(records, permissions)
	if err := s.present(ctx, records); err != nil {
		return nil, err
	}
	return build(records)
}
func filterByPermissions(records []Record, permissions map[string]struct{}) []Record {
	byID := make(map[string]Record, len(records))
	candidates := map[string]struct{}{}
	for _, record := range records {
		byID[record.ID] = record
		if record.PermissionID == nil {
			candidates[record.ID] = struct{}{}
		} else if _, ok := permissions[*record.PermissionID]; ok {
			candidates[record.ID] = struct{}{}
		}
	}
	allowed := map[string]struct{}{}
	for id := range candidates {
		chain := []string{id}
		current := byID[id]
		valid := true
		visited := map[string]struct{}{id: {}}
		for current.ParentID != nil {
			parent, exists := byID[*current.ParentID]
			if !exists {
				valid = false
				break
			}
			if _, cycle := visited[parent.ID]; cycle {
				valid = false
				break
			}
			visited[parent.ID] = struct{}{}
			if parent.PermissionID != nil {
				if _, ok := permissions[*parent.PermissionID]; !ok {
					valid = false
				}
			}
			chain = append(chain, parent.ID)
			current = parent
		}
		if valid {
			for _, item := range chain {
				allowed[item] = struct{}{}
			}
		}
	}
	filtered := make([]Record, 0, len(allowed))
	for _, record := range records {
		if _, ok := allowed[record.ID]; ok {
			filtered = append(filtered, record)
		}
	}
	return filtered
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
func (s *Service) list(ctx context.Context, visibleOnly bool) ([]Record, error) {
	cacheKey := menuCacheAll
	if visibleOnly {
		cacheKey = menuCacheVisible
	}
	if s.cache != nil {
		if records, err := cache.GetJSON[[]Record](ctx, s.cache, cacheKey); err == nil {
			if len(records) > s.cfg.Menu.MaxNodes {
				return nil, fmt.Errorf("%w: menu tree exceeds %d nodes", ErrConflict, s.cfg.Menu.MaxNodes)
			}
			if records == nil {
				return []Record{}, nil
			}
			return records, nil
		} else if !errors.Is(err, cache.ErrMiss) && s.logger != nil {
			s.logger.WarnContext(ctx, "read menu cache", "key", cacheKey, "error", err)
		}
	}
	where := `deleted_at IS NULL`
	if visibleOnly {
		where += ` AND visible=true AND status='active'`
	}
	records := []Record{}
	query := fmt.Sprintf(`SELECT %s FROM menus WHERE %s ORDER BY sort_order,id LIMIT %d`, columns, where, s.cfg.Menu.MaxNodes+1)
	err := s.db.SelectContext(ctx, &records, query)
	if err != nil {
		return nil, err
	}
	if len(records) > s.cfg.Menu.MaxNodes {
		return nil, fmt.Errorf("%w: menu tree exceeds %d nodes", ErrConflict, s.cfg.Menu.MaxNodes)
	}
	if s.cache != nil {
		if err := cache.SetJSON(ctx, s.cache, cacheKey, records, s.cfg.Menu.CacheTTL); err != nil && s.logger != nil {
			s.logger.WarnContext(ctx, "write menu cache", "key", cacheKey, "error", err)
		}
	}
	return records, nil
}
func build(records []Record) ([]*Node, error) {
	forest, err := platformtree.Build(records, func(v Record) string { return v.ID }, func(v Record) (string, bool) {
		if v.ParentID == nil {
			return "", false
		}
		return *v.ParentID, true
	})
	if err != nil {
		return nil, fmt.Errorf("build menu tree: %w", err)
	}
	var convert func([]*platformtree.Node[Record]) []*Node
	convert = func(source []*platformtree.Node[Record]) []*Node {
		out := make([]*Node, len(source))
		for i, item := range source {
			out[i] = &Node{Record: item.Value, Children: convert(item.Children)}
		}
		return out
	}
	return convert(forest), nil
}
func (s *Service) Update(ctx context.Context, input UpdateInput) (Record, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Record{}, err
	}
	input.ID = strings.TrimSpace(input.ID)
	input.ParentID = clean(input.ParentID)
	input.PermissionID = clean(input.PermissionID)
	if input.ID == "" || len(input.ID) > maxMenuIDLength || input.Version <= 0 {
		return Record{}, ErrInvalid
	}
	existing, err := s.Get(ctx, input.ID)
	if err != nil {
		return Record{}, err
	}
	candidate := normalizeInput(Input{ParentID: input.ParentID, Key: existing.Key, Name: input.Name, Type: input.Type, RoutePath: input.RoutePath, Component: input.Component, ExternalURL: input.ExternalURL, Icon: input.Icon, PermissionID: input.PermissionID, Visible: input.Visible, Status: input.Status, SortOrder: input.SortOrder, Metadata: input.Metadata})
	if len(candidate.Metadata) == 0 {
		candidate.Metadata = json.RawMessage(`{}`)
	}
	if err := validate(candidate); err != nil {
		return Record{}, err
	}
	err = s.mutate(ctx, "platform.menu.update", input.ID, input, func(lockCtx context.Context, tx *sqlx.Tx) error {
		if err := s.validateReferences(lockCtx, tx, input.ID, candidate.Type, candidate.ParentID, candidate.PermissionID); err != nil {
			return err
		}
		result, err := tx.ExecContext(lockCtx, tx.Rebind(`UPDATE menus SET parent_id=?,name=?,menu_type=?,route_path=?,component=?,external_url=?,icon=?,permission_id=?,visible=?,status=?,sort_order=?,metadata=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`), candidate.ParentID, candidate.Name, candidate.Type, candidate.RoutePath, candidate.Component, candidate.ExternalURL, candidate.Icon, candidate.PermissionID, candidate.Visible, candidate.Status, candidate.SortOrder, string(candidate.Metadata), time.Now(), actor.ID, input.ID, input.Version)
		if err != nil {
			return err
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("update menu affected rows: %w", rowsErr)
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
	if id == "" || len(id) > maxMenuIDLength || version <= 0 {
		return ErrInvalid
	}
	return s.mutate(ctx, "platform.menu.delete", id, map[string]any{"version": version}, func(lockCtx context.Context, tx *sqlx.Tx) error {
		var children int
		if err := tx.GetContext(lockCtx, &children, tx.Rebind(`SELECT count(*) FROM menus WHERE parent_id=? AND deleted_at IS NULL`), id); err != nil {
			return err
		}
		if children > 0 {
			return ErrConflict
		}
		now := time.Now()
		result, err := tx.ExecContext(lockCtx, tx.Rebind(`UPDATE menus SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`), now, actor.ID, now, actor.ID, id, version)
		if err != nil {
			return err
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("delete menu affected rows: %w", rowsErr)
		}
		if rows != 1 {
			return ErrConflict
		}
		return nil
	})
}
func (s *Service) validateReferences(ctx context.Context, tx *sqlx.Tx, id, menuType string, parent, permission *string) error {
	if permission != nil {
		var count int
		if err := tx.GetContext(ctx, &count, tx.Rebind(`SELECT count(*) FROM permissions WHERE id=? AND node_type='permission' AND status='active' AND deleted_at IS NULL`), *permission); err != nil {
			return err
		}
		if count != 1 {
			return ErrInvalid
		}
	}
	if parent != nil && *parent == id {
		return ErrInvalid
	}
	var records []Record
	query := fmt.Sprintf(`SELECT %s FROM menus WHERE deleted_at IS NULL ORDER BY sort_order,id LIMIT %d`, columns, s.cfg.Menu.MaxNodes+1)
	if err := tx.SelectContext(ctx, &records, query); err != nil {
		return err
	}
	if len(records) > s.cfg.Menu.MaxNodes || (id == "" && len(records) == s.cfg.Menu.MaxNodes) {
		return ErrConflict
	}
	found := id == ""
	for i := range records {
		if records[i].ID == id {
			records[i].ParentID = parent
			records[i].Type = menuType
			found = true
		}
	}
	if !found {
		return ErrNotFound
	}
	if id == "" {
		records = append(records, Record{ID: "__candidate__", ParentID: parent, Type: menuType})
	}
	if !validParentTypes(records) {
		return ErrInvalid
	}
	_, err := build(records)
	if err != nil {
		return ErrInvalid
	}
	return nil
}
func (s *Service) mutate(ctx context.Context, operation, id string, request any, fn func(context.Context, *sqlx.Tx) error) error {
	run := func(runCtx context.Context) error {
		started := time.Now()
		operationEntry := operationlog.Entry{Operation: operation, ResourceType: "menu", ResourceID: id, Source: "backend", Protocol: "service", Request: request}
		securityEntry := securitylog.Entry{EventType: securitylog.EventMenuChanged, SubjectID: id, SubjectType: "menu", Metadata: map[string]any{"operation": operation}}
		err := s.tx.Within(runCtx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
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
		s.invalidateCache(runCtx)
		return nil
	}
	if s.locker == nil {
		return run(ctx)
	}
	var businessErr error
	err := cache.WithLock(ctx, s.locker, "menu:tree", s.cfg.DistributedLock.TTL, s.cfg.DistributedLock.RetryDelay, func(lockCtx context.Context) error {
		businessErr = run(lockCtx)
		return businessErr
	})
	if businessErr != nil {
		return businessErr
	}
	if err != nil {
		return ErrConflict
	}
	return nil
}

func validParentTypes(records []Record) bool {
	byID := make(map[string]Record, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	for _, record := range records {
		if record.ParentID == nil {
			if record.Type == "button" {
				return false
			}
			continue
		}
		parent, ok := byID[*record.ParentID]
		if !ok || (record.Type == "button" && parent.Type != "page") || (record.Type != "button" && parent.Type != "directory") {
			return false
		}
	}
	return true
}

func normalizeInput(input Input) Input {
	input.Key = strings.ToLower(strings.TrimSpace(input.Key))
	input.Name = strings.TrimSpace(input.Name)
	input.Type = strings.TrimSpace(input.Type)
	input.RoutePath = strings.TrimSpace(input.RoutePath)
	input.Component = strings.TrimSpace(input.Component)
	input.ExternalURL = strings.TrimSpace(input.ExternalURL)
	input.Icon = strings.TrimSpace(input.Icon)
	input.Status = strings.TrimSpace(input.Status)
	input.ParentID = clean(input.ParentID)
	input.PermissionID = clean(input.PermissionID)
	return input
}

func validMenuID(value *string) bool {
	return value == nil || (*value != "" && len(*value) <= maxMenuIDLength)
}

func validMetadata(value json.RawMessage) bool {
	if !json.Valid(value) {
		return false
	}
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}

func validRoutePath(value string) bool {
	return routePathPattern.MatchString(value) && !strings.Contains(value, "//") && !strings.Contains(value, "..")
}

func validComponent(value string) bool {
	return componentPattern.MatchString(value) && !strings.HasPrefix(value, "/") && !strings.Contains(value, "..") && !strings.Contains(value, "//")
}

func (s *Service) invalidateCache(ctx context.Context) {
	if s.cache == nil {
		return
	}
	cacheCtx, cancel := cache.AfterCommitContext(ctx)
	defer cancel()
	if err := s.cache.Delete(cacheCtx, menuCacheAll, menuCacheVisible); err != nil && s.logger != nil {
		s.logger.WarnContext(cacheCtx, "invalidate menu cache", "error", err)
	}
}
func clean(value *string) *string {
	if value == nil {
		return nil
	}
	cleaned := strings.TrimSpace(*value)
	if cleaned == "" {
		return nil
	}
	return &cleaned
}
