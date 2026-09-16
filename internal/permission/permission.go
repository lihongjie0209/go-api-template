package permission

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/routepolicy"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	platformtree "github.com/lihongjie0209/microservice-platform-go/tree"
)

var (
	ErrInvalid     = errors.New("invalid permission")
	ErrNotFound    = errors.New("permission not found")
	ErrConflict    = errors.New("permission conflict")
	ErrHasChildren = errors.New("permission has children")
	ErrInUse       = errors.New("permission is in use")
)
var validKey = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{2,127}$`)

type Record struct {
	ID          string    `db:"id" json:"id"`
	ParentID    *string   `db:"parent_id" json:"parent_id"`
	Key         string    `db:"permission_key" json:"permission_key"`
	Name        string    `db:"name" json:"name"`
	NodeType    string    `db:"node_type" json:"node_type"`
	Resource    string    `db:"resource" json:"resource"`
	Action      string    `db:"action" json:"action"`
	Description string    `db:"description" json:"description"`
	SortOrder   int64     `db:"sort_order" json:"sort_order"`
	Status      string    `db:"status" json:"status"`
	IsSystem    bool      `db:"is_system" json:"is_system"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	CreatedBy   string    `db:"created_by" json:"created_by"`
	UpdatedAt   time.Time `db:"updated_at" json:"updated_at"`
	UpdatedBy   string    `db:"updated_by" json:"updated_by"`
	Version     int64     `db:"version" json:"version"`
}
type TreeNode struct {
	Record
	Children []*TreeNode `json:"children"`
}
type Input struct {
	ParentID                                                   *string
	Key, Name, NodeType, Resource, Action, Description, Status string
	SortOrder                                                  int64
}
type TreeInput struct {
	Keyword   string
	NodeTypes []string
	Statuses  []string
}

type Repository struct{ db *sqlx.DB }

func NewRepository(db *sqlx.DB) *Repository { return &Repository{db: db} }

const columns = `id, parent_id, permission_key, name, node_type, resource, action, description, sort_order, status, is_system, created_at, created_by, updated_at, updated_by, version`

func (r *Repository) Get(ctx context.Context, id string) (Record, error) {
	var v Record
	if err := r.db.GetContext(ctx, &v, r.db.Rebind(`SELECT `+columns+` FROM permissions WHERE id=? AND deleted_at IS NULL`), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return v, ErrNotFound
		}
		return v, fmt.Errorf("get permission: %w", err)
	}
	return v, nil
}
func (r *Repository) List(ctx context.Context) ([]Record, error) {
	var v []Record
	if err := r.db.SelectContext(ctx, &v, `SELECT `+columns+` FROM permissions WHERE deleted_at IS NULL ORDER BY sort_order,id`); err != nil {
		return nil, fmt.Errorf("list permissions: %w", err)
	}
	return v, nil
}

type Service struct {
	repository *Repository
	transactor *database.Transactor
	policies   policyRefresher
	operations operationlog.Recorder
	security   securitylog.Recorder
	logger     *slog.Logger
}

type policyRefresher interface {
	Refresh(context.Context) error
	Notify(context.Context) error
	Invalidate()
}

func New(repository *Repository, transactor *database.Transactor, policies *routepolicy.Manager, operations operationlog.Recorder, security securitylog.Recorder, logger *slog.Logger) *Service {
	return &Service{repository: repository, transactor: transactor, policies: policies, operations: operations, security: security, logger: logger}
}
func (s *Service) Get(ctx context.Context, id string) (Record, error) {
	return s.repository.Get(ctx, id)
}
func (s *Service) Tree(ctx context.Context, input TreeInput) ([]*TreeNode, error) {
	if len(input.NodeTypes) > 20 || len(input.Statuses) > 20 || !validFilters(input) {
		return nil, ErrInvalid
	}
	records, err := s.repository.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(records) > 10000 {
		return nil, fmt.Errorf("%w: tree exceeds 10000 nodes", ErrInvalid)
	}
	records = filterRecords(records, input)
	forest, err := platformtree.Build(records, func(v Record) string { return v.ID }, func(v Record) (string, bool) {
		if v.ParentID == nil {
			return "", false
		}
		return *v.ParentID, true
	})
	if err != nil {
		return nil, fmt.Errorf("build permission tree: %w", err)
	}
	return mapTree(forest), nil
}
func validFilters(input TreeInput) bool {
	for _, value := range input.NodeTypes {
		if value != "group" && value != "permission" {
			return false
		}
	}
	for _, value := range input.Statuses {
		if value != "active" && value != "disabled" {
			return false
		}
	}
	return true
}
func filterRecords(records []Record, input TreeInput) []Record {
	keyword := strings.ToLower(strings.TrimSpace(input.Keyword))
	types, statuses := make(map[string]struct{}, len(input.NodeTypes)), make(map[string]struct{}, len(input.Statuses))
	for _, value := range input.NodeTypes {
		types[value] = struct{}{}
	}
	for _, value := range input.Statuses {
		statuses[value] = struct{}{}
	}
	byID, keep := make(map[string]Record, len(records)), make(map[string]struct{})
	for _, record := range records {
		byID[record.ID] = record
	}
	for _, record := range records {
		if len(types) > 0 {
			if _, ok := types[record.NodeType]; !ok {
				continue
			}
		}
		if len(statuses) > 0 {
			if _, ok := statuses[record.Status]; !ok {
				continue
			}
		}
		if keyword != "" && !strings.Contains(strings.ToLower(strings.Join([]string{record.Key, record.Name, record.Resource, record.Action, record.Description}, "\x00")), keyword) {
			continue
		}
		current := record
		visited := map[string]struct{}{}
		for {
			if _, exists := visited[current.ID]; exists {
				break
			}
			visited[current.ID], keep[current.ID] = struct{}{}, struct{}{}
			if current.ParentID == nil {
				break
			}
			parent, exists := byID[*current.ParentID]
			if !exists {
				break
			}
			current = parent
		}
	}
	out := make([]Record, 0, len(keep))
	for _, record := range records {
		if _, ok := keep[record.ID]; ok {
			out = append(out, record)
		}
	}
	return out
}
func mapTree(nodes []*platformtree.Node[Record]) []*TreeNode {
	out := make([]*TreeNode, len(nodes))
	for i, node := range nodes {
		out[i] = &TreeNode{Record: node.Value, Children: mapTree(node.Children)}
	}
	return out
}
func validate(input Input) error {
	input.Key = strings.TrimSpace(input.Key)
	if !validKey.MatchString(input.Key) || strings.TrimSpace(input.Name) == "" || (input.NodeType != "group" && input.NodeType != "permission") || (input.Status != "active" && input.Status != "disabled") {
		return ErrInvalid
	}
	if input.NodeType == "permission" && (input.Resource == "" || input.Action == "") {
		return ErrInvalid
	}
	if input.NodeType == "group" && (input.Resource != "" || input.Action != "") {
		return ErrInvalid
	}
	return nil
}
func actor(ctx context.Context) (string, error) {
	p, ok := platformprincipal.FromContext(ctx)
	if !ok || p.ID == "" {
		return "", platformprincipal.ErrMissing
	}
	return p.ID, nil
}
func (s *Service) Create(ctx context.Context, input Input) (Record, error) {
	a, e := actor(ctx)
	if e != nil {
		return Record{}, e
	}
	if e = validate(input); e != nil {
		return Record{}, e
	}
	input = normalize(input)
	id := uuid.NewString()
	now := time.Now()
	e = s.mutate(ctx, "permission.create", id, input, func(tx *sqlx.Tx) error {
		if err := s.validateTreeTx(ctx, tx, "", input.ParentID); err != nil {
			return err
		}
		q := tx.Rebind(`INSERT INTO permissions (id,parent_id,permission_key,name,node_type,resource,action,description,sort_order,status,is_system,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,?,?,?,?,?,false,?,?,?,?,1)`)
		_, err := tx.ExecContext(ctx, q, id, input.ParentID, input.Key, input.Name, input.NodeType, input.Resource, input.Action, input.Description, input.SortOrder, input.Status, now, a, now, a)
		return err
	})
	if e != nil {
		return Record{}, fmt.Errorf("create permission: %w", e)
	}
	return s.repository.Get(ctx, id)
}
func (s *Service) validateTreeTx(ctx context.Context, tx *sqlx.Tx, id string, parent *string) error {
	var records []Record
	e := tx.SelectContext(ctx, &records, `SELECT `+columns+` FROM permissions WHERE deleted_at IS NULL ORDER BY sort_order,id`)
	if e != nil {
		return e
	}
	if parent != nil {
		validParent := false
		for _, record := range records {
			if record.ID == *parent && record.NodeType == "group" {
				validParent = true
				break
			}
		}
		if !validParent {
			return ErrInvalid
		}
	}
	if id != "" {
		found := false
		for i := range records {
			if records[i].ID == id {
				records[i].ParentID = parent
				found = true
			}
		}
		if !found {
			return ErrNotFound
		}
	} else if parent != nil {
		records = append(records, Record{ID: "__candidate__", ParentID: parent})
	}
	if !hasValidParentTypes(records) {
		return ErrInvalid
	}
	_, e = platformtree.Build(records, func(v Record) string { return v.ID }, func(v Record) (string, bool) {
		if v.ParentID == nil {
			return "", false
		}
		return *v.ParentID, true
	})
	if e != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, e)
	}
	return nil
}
func hasValidParentTypes(records []Record) bool {
	byID := make(map[string]Record, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	for _, record := range records {
		if record.ParentID == nil {
			continue
		}
		parent, exists := byID[*record.ParentID]
		if !exists || parent.NodeType != "group" {
			return false
		}
	}
	return true
}
func (s *Service) Update(ctx context.Context, id string, version int64, input Input) (Record, error) {
	a, e := actor(ctx)
	if e != nil {
		return Record{}, e
	}
	if version <= 0 {
		return Record{}, ErrInvalid
	}
	if e = validate(input); e != nil {
		return Record{}, e
	}
	if input.ParentID != nil && *input.ParentID == id {
		return Record{}, ErrInvalid
	}
	input = normalize(input)
	e = s.mutate(ctx, "permission.update", id, input, func(tx *sqlx.Tx) error {
		current, err := getForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Version != version {
			return ErrConflict
		}
		if err := s.validateTreeTx(ctx, tx, id, input.ParentID); err != nil {
			return err
		}
		if current.Key != input.Key || current.NodeType != input.NodeType || (current.Status == "active" && input.Status != "active") {
			used, refErr := routePolicyReferenceCount(ctx, tx, id)
			if refErr != nil {
				return refErr
			}
			if used > 0 {
				return ErrInUse
			}
		}
		q := tx.Rebind(`UPDATE permissions SET parent_id=?,permission_key=?,name=?,node_type=?,resource=?,action=?,description=?,sort_order=?,status=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`)
		res, err := tx.ExecContext(ctx, q, input.ParentID, input.Key, input.Name, input.NodeType, input.Resource, input.Action, input.Description, input.SortOrder, input.Status, time.Now(), a, id, version)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("permission update affected rows: %w", err)
		}
		if n != 1 {
			return ErrConflict
		}
		return nil
	})
	if e != nil {
		return Record{}, e
	}
	return s.repository.Get(ctx, id)
}
func (s *Service) Delete(ctx context.Context, id string, version int64) error {
	a, e := actor(ctx)
	if e != nil {
		return e
	}
	if id == "" || version <= 0 {
		return ErrInvalid
	}
	return s.mutate(ctx, "permission.delete", id, map[string]any{"version": version}, func(tx *sqlx.Tx) error {
		current, err := getForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Version != version {
			return ErrConflict
		}
		if current.IsSystem {
			return ErrConflict
		}
		children, err := referenceCount(ctx, tx, `SELECT count(*) FROM permissions WHERE parent_id=? AND deleted_at IS NULL`, id)
		if err != nil {
			return err
		}
		if children > 0 {
			return ErrHasChildren
		}
		used, err := permissionReferenceCount(ctx, tx, id)
		if err != nil {
			return err
		}
		if used > 0 {
			return ErrInUse
		}
		q := tx.Rebind(`UPDATE permissions SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL AND is_system=false`)
		now := time.Now()
		res, err := tx.ExecContext(ctx, q, now, a, now, a, id, version)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("permission delete affected rows: %w", err)
		}
		if n != 1 {
			return ErrConflict
		}
		return nil
	})
}

func normalize(input Input) Input {
	input.Key = strings.ToLower(strings.TrimSpace(input.Key))
	input.Name, input.Resource, input.Action, input.Description = strings.TrimSpace(input.Name), strings.TrimSpace(input.Resource), strings.TrimSpace(input.Action), strings.TrimSpace(input.Description)
	if input.ParentID != nil {
		value := strings.TrimSpace(*input.ParentID)
		if value == "" {
			input.ParentID = nil
		} else {
			input.ParentID = &value
		}
	}
	return input
}
func getForUpdate(ctx context.Context, tx *sqlx.Tx, id string) (Record, error) {
	var record Record
	err := tx.GetContext(ctx, &record, tx.Rebind(`SELECT `+columns+` FROM permissions WHERE id=? AND deleted_at IS NULL FOR UPDATE`), id)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	if err != nil {
		return record, fmt.Errorf("lock permission: %w", err)
	}
	return record, nil
}
func referenceCount(ctx context.Context, tx *sqlx.Tx, query, id string) (int, error) {
	var count int
	if err := tx.GetContext(ctx, &count, tx.Rebind(query), id); err != nil {
		return 0, err
	}
	return count, nil
}
func routePolicyReferenceCount(ctx context.Context, tx *sqlx.Tx, id string) (int, error) {
	return referenceCount(ctx, tx, `SELECT count(*) FROM route_policy_permission_refs r JOIN route_policy_definitions p ON p.id=r.policy_id AND p.deleted_at IS NULL AND p.status='active' WHERE r.permission_id=? AND r.deleted_at IS NULL`, id)
}
func permissionReferenceCount(ctx context.Context, tx *sqlx.Tx, id string) (int, error) {
	queries := []string{
		`SELECT count(*) FROM menus WHERE permission_id=? AND deleted_at IS NULL`,
		`SELECT count(*) FROM tenant_permission_grants WHERE permission_id=? AND deleted_at IS NULL`,
		`SELECT count(*) FROM tenant_role_permissions WHERE permission_id=? AND deleted_at IS NULL`,
		`SELECT count(*) FROM route_policy_permission_refs WHERE permission_id=? AND deleted_at IS NULL`,
	}
	for _, query := range queries {
		count, err := referenceCount(ctx, tx, query, id)
		if err != nil {
			return 0, err
		}
		if count > 0 {
			return count, nil
		}
	}
	return 0, nil
}
func (s *Service) mutate(ctx context.Context, operation, id string, request any, fn func(*sqlx.Tx) error) error {
	committed := false
	err := operationlog.Do(ctx, s.operations, operationlog.Entry{Operation: operation, ResourceType: "permission", ResourceID: id, Source: "backend", Protocol: "service", Request: request}, func() error {
		txErr := s.transactor.Within(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, fn)
		committed = txErr == nil
		return txErr
	})
	entry := securitylog.Entry{EventType: securitylog.EventPermissionChanged, SubjectID: id, SubjectType: "permission", Succeeded: committed, Metadata: map[string]any{"operation": operation}}
	securityErr := s.security.Record(ctx, entry)
	if !committed {
		return err
	}
	if refreshErr := s.policies.Refresh(ctx); refreshErr != nil {
		s.policies.Invalidate()
		s.logger.Error("refresh route policies after permission change", "permission_id", id, "error", refreshErr)
	}
	if notifyErr := s.policies.Notify(ctx); notifyErr != nil {
		s.logger.Warn("publish permission policy refresh", "permission_id", id, "error", notifyErr)
	}
	if securityErr != nil && err == nil && s.security.FailClosed() {
		return securityErr
	}
	return err
}
