package navigation

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

const columns = `id,application_id,parent_id,navigation_key,name,navigation_type,route_path,component,icon,resource,action,visible,status,sort_order,metadata,created_at,created_by,updated_at,updated_by,version`
const maxTreeNodes = 5000

type UpdateInput struct {
	ID, Name, Type, RoutePath, Component, Icon, Resource, Action, Status string
	ParentID                                                             *string
	Visible                                                              bool
	SortOrder, Version                                                   int64
	Metadata                                                             []byte
}
type TreeInput struct {
	ApplicationID, Keyword string
	Types, Statuses        []string
}

type Service struct {
	db         *sqlx.DB
	tx         *database.Transactor
	resources  *pbac.Registry
	operations operationlog.TransactionalRecorder
	actors     presentation.ActorResolver
}

func New(db *sqlx.DB, tx *database.Transactor, resources *pbac.Registry, operations operationlog.TransactionalRecorder, actors presentation.ActorResolver) *Service {
	return &Service{db: db, tx: tx, resources: resources, operations: operations, actors: actors}
}

func (s *Service) Create(ctx context.Context, input Input) (Record, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Record{}, err
	}
	input = Normalize(input)
	var code string
	if err := s.db.GetContext(ctx, &code, s.db.Rebind(`SELECT code FROM applications WHERE id=? AND deleted_at IS NULL`), input.ApplicationID); errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrInvalid
	} else if err != nil {
		return Record{}, err
	}
	input.ApplicationCode = code
	if err := Validate(input, s.resources); err != nil {
		return Record{}, err
	}
	id, err := StableID(code, input.Key)
	if err != nil {
		return Record{}, err
	}
	err = s.mutate(ctx, "platform.navigation.create", id, input.Name, input, func(tx *sqlx.Tx) error {
		code, err := s.applicationCode(ctx, tx, input.ApplicationID)
		if err != nil {
			return err
		}
		if code != input.ApplicationCode {
			return ErrConflict
		}
		if err := s.validateTree(ctx, tx, input.ApplicationID, "", Record{ID: id, ApplicationID: input.ApplicationID, ParentID: input.ParentID, Key: input.Key, Name: input.Name, Type: input.Type}); err != nil {
			return err
		}
		now := time.Now()
		_, err = tx.ExecContext(ctx, tx.Rebind(`INSERT INTO navigations(id,application_id,parent_id,navigation_key,name,navigation_type,route_path,component,icon,resource,action,visible,status,sort_order,metadata,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1)`), id, input.ApplicationID, input.ParentID, input.Key, input.Name, input.Type, input.RoutePath, input.Component, input.Icon, input.Resource, input.Action, input.Visible, input.Status, input.SortOrder, string(input.Metadata), now, actor.ID, now, actor.ID)
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
	err := s.db.GetContext(ctx, &record, s.db.Rebind(`SELECT `+columns+` FROM navigations WHERE id=? AND deleted_at IS NULL`), strings.TrimSpace(id))
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

func (s *Service) Tree(ctx context.Context, input TreeInput) ([]*Node, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return nil, err
	}
	input.ApplicationID = strings.TrimSpace(input.ApplicationID)
	input.Keyword = strings.TrimSpace(input.Keyword)
	if !validTreeInput(input) {
		return nil, ErrInvalid
	}
	records := []Record{}
	if err := s.db.SelectContext(ctx, &records, s.db.Rebind(`SELECT `+columns+` FROM navigations WHERE application_id=? AND deleted_at IS NULL ORDER BY sort_order,id LIMIT ?`), input.ApplicationID, maxTreeNodes+1); err != nil {
		return nil, err
	}
	if len(records) > maxTreeNodes {
		return nil, ErrConflict
	}
	records = filter(records, input)
	if err := s.present(ctx, records); err != nil {
		return nil, err
	}
	return Build(records)
}

// CurrentTree reads the source tree only through an active tenant grant and an
// active membership. This SQL boundary prevents a caller from using a globally
// visible application ID to enumerate another tenant's navigation.
func (s *Service) CurrentTree(ctx context.Context, applicationID string) ([]*Node, error) {
	actor, err := platformprincipal.Require(ctx)
	applicationID = strings.TrimSpace(applicationID)
	if err != nil || actor.Type != platformprincipal.TypeUser || actor.TenantID == "" || actor.MembershipID == "" || applicationID == "" || len(applicationID) > 128 {
		return nil, ErrInvalid
	}
	records := []Record{}
	now := time.Now()
	query := `SELECT ` + prefixedColumns("n") + ` FROM navigations n JOIN applications a ON a.id=n.application_id AND a.status='active' AND a.deleted_at IS NULL JOIN tenant_application_grants g ON g.application_id=a.id AND g.tenant_id=? AND g.status='active' AND g.deleted_at IS NULL AND (g.starts_at IS NULL OR g.starts_at<=?) AND (g.expires_at IS NULL OR g.expires_at>?) JOIN tenant_memberships m ON m.tenant_id=g.tenant_id AND m.id=? AND m.user_id=? AND m.status='active' AND m.deleted_at IS NULL WHERE n.application_id=? AND n.status='active' AND n.visible=true AND n.deleted_at IS NULL ORDER BY n.sort_order,n.id LIMIT ?`
	if err := s.db.SelectContext(ctx, &records, s.db.Rebind(query), actor.TenantID, now, now, actor.MembershipID, actor.ID, applicationID, maxTreeNodes+1); err != nil {
		return nil, err
	}
	if len(records) > maxTreeNodes {
		return nil, ErrConflict
	}
	if err := s.present(ctx, records); err != nil {
		return nil, err
	}
	return Build(records)
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (Record, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return Record{}, err
	}
	existing, err := s.Get(ctx, input.ID)
	if err != nil {
		return Record{}, err
	}
	candidate := Normalize(Input{ApplicationID: existing.ApplicationID, Key: existing.Key, Name: input.Name, Type: input.Type, ParentID: input.ParentID, RoutePath: input.RoutePath, Component: input.Component, Icon: input.Icon, Resource: input.Resource, Action: input.Action, Visible: input.Visible, Status: input.Status, SortOrder: input.SortOrder, Metadata: input.Metadata})
	if input.Version <= 0 || Validate(candidate, s.resources) != nil {
		return Record{}, ErrInvalid
	}
	err = s.mutate(ctx, "platform.navigation.update", input.ID, candidate.Name, input, func(tx *sqlx.Tx) error {
		if err := s.validateTree(ctx, tx, existing.ApplicationID, input.ID, Record{ID: input.ID, ApplicationID: existing.ApplicationID, ParentID: candidate.ParentID, Key: existing.Key, Name: candidate.Name, Type: candidate.Type}); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE navigations SET parent_id=?,name=?,navigation_type=?,route_path=?,component=?,icon=?,resource=?,action=?,visible=?,status=?,sort_order=?,metadata=?,updated_at=?,updated_by=? WHERE id=? AND version=? AND application_id=? AND deleted_at IS NULL`), candidate.ParentID, candidate.Name, candidate.Type, candidate.RoutePath, candidate.Component, candidate.Icon, candidate.Resource, candidate.Action, candidate.Visible, candidate.Status, candidate.SortOrder, string(candidate.Metadata), time.Now(), actor.ID, input.ID, input.Version, existing.ApplicationID)
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
	return s.mutate(ctx, "platform.navigation.delete", id, current.Name, map[string]any{"version": version}, func(tx *sqlx.Tx) error {
		var children int
		if err := tx.GetContext(ctx, &children, tx.Rebind(`SELECT count(*) FROM navigations WHERE parent_id=? AND application_id=? AND deleted_at IS NULL`), id, current.ApplicationID); err != nil {
			return err
		}
		if children > 0 {
			return ErrConflict
		}
		result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE navigations SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=? WHERE id=? AND version=? AND application_id=? AND deleted_at IS NULL`), time.Now(), actor.ID, time.Now(), actor.ID, id, version, current.ApplicationID)
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

func (s *Service) applicationCode(ctx context.Context, tx *sqlx.Tx, id string) (string, error) {
	var code string
	err := tx.GetContext(ctx, &code, tx.Rebind(`SELECT code FROM applications WHERE id=? AND deleted_at IS NULL FOR UPDATE`), id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalid
	}
	return code, err
}
func (s *Service) validateTree(ctx context.Context, tx *sqlx.Tx, applicationID, replacedID string, candidate Record) error {
	records := []Record{}
	if err := tx.SelectContext(ctx, &records, tx.Rebind(`SELECT `+columns+` FROM navigations WHERE application_id=? AND deleted_at IS NULL ORDER BY sort_order,id LIMIT ? FOR UPDATE`), applicationID, maxTreeNodes+1); err != nil {
		return err
	}
	if len(records) > maxTreeNodes {
		return ErrConflict
	}
	replaced := false
	for i := range records {
		if records[i].ID == replacedID {
			records[i] = candidate
			replaced = true
		}
	}
	if replacedID != "" && !replaced {
		return ErrNotFound
	}
	if replacedID == "" {
		records = append(records, candidate)
	}
	_, err := Build(records)
	return err
}
func (s *Service) mutate(ctx context.Context, operation, id, name string, request any, fn func(*sqlx.Tx) error) error {
	started := time.Now()
	entry := operationlog.Entry{Operation: operation, ResourceType: "navigation", ResourceID: id, ResourceName: name, Source: "backend", Protocol: "service", Request: request}
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
	for _, r := range records {
		ids = append(ids, r.CreatedBy, r.UpdatedBy)
	}
	names, err := presentation.ActorNames(ctx, s.actors, ids...)
	if err != nil {
		return err
	}
	for i := range records {
		records[i].CreatedByName = names[records[i].CreatedBy]
		records[i].UpdatedByName = names[records[i].UpdatedBy]
		records[i].CreatedAt = presentation.Time(records[i].CreatedAt)
		records[i].UpdatedAt = presentation.Time(records[i].UpdatedAt)
	}
	return nil
}

func validTreeInput(input TreeInput) bool {
	if input.ApplicationID == "" || len(input.ApplicationID) > 128 || len(input.Keyword) > 256 || len(input.Types) > 2 || len(input.Statuses) > 2 {
		return false
	}
	for _, navigationType := range input.Types {
		if navigationType != "directory" && navigationType != "menu" {
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
func filter(records []Record, input TreeInput) []Record {
	if input.Keyword == "" && len(input.Types) == 0 && len(input.Statuses) == 0 {
		return records
	}
	allowedType := set(input.Types)
	allowedStatus := set(input.Statuses)
	byID := map[string]Record{}
	matched := map[string]struct{}{}
	for _, r := range records {
		byID[r.ID] = r
		if len(allowedType) > 0 && !has(allowedType, r.Type) || len(allowedStatus) > 0 && !has(allowedStatus, r.Status) {
			continue
		}
		if input.Keyword == "" || strings.Contains(strings.ToLower(r.Key), strings.ToLower(input.Keyword)) || strings.Contains(strings.ToLower(r.Name), strings.ToLower(input.Keyword)) {
			matched[r.ID] = struct{}{}
		}
	}
	allowed := map[string]struct{}{}
	for id := range matched {
		for id != "" {
			if _, ok := allowed[id]; ok {
				break
			}
			allowed[id] = struct{}{}
			r, ok := byID[id]
			if !ok || r.ParentID == nil {
				break
			}
			id = *r.ParentID
		}
	}
	result := []Record{}
	for _, r := range records {
		if has(allowed, r.ID) {
			result = append(result, r)
		}
	}
	return result
}
func set(values []string) map[string]struct{} {
	result := map[string]struct{}{}
	for _, v := range values {
		result[v] = struct{}{}
	}
	return result
}
func has(values map[string]struct{}, value string) bool { _, ok := values[value]; return ok }

func prefixedColumns(alias string) string {
	parts := strings.Split(columns, ",")
	for index := range parts {
		parts[index] = alias + "." + parts[index]
	}
	return strings.Join(parts, ",")
}
