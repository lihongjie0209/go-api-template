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
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/lihongjie0209/microservice-platform-go/stableid"
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

const permissionSeedNamespace = "ec62a525-a494-4f0f-bf19-0e982c1a6f6f"

type Record struct {
	ID            string    `db:"id" json:"id"`
	ParentID      *string   `db:"parent_id" json:"parent_id"`
	Key           string    `db:"permission_key" json:"permission_key"`
	Name          string    `db:"name" json:"name"`
	NodeType      string    `db:"node_type" json:"node_type"`
	Resource      string    `db:"resource" json:"resource"`
	Action        string    `db:"action" json:"action"`
	Description   string    `db:"description" json:"description"`
	SortOrder     int64     `db:"sort_order" json:"sort_order"`
	Status        string    `db:"status" json:"status"`
	IsSystem      bool      `db:"is_system" json:"is_system"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
	CreatedBy     string    `db:"created_by" json:"created_by"`
	CreatedByName string    `db:"-" json:"created_by_name"`
	UpdatedAt     time.Time `db:"updated_at" json:"updated_at"`
	UpdatedBy     string    `db:"updated_by" json:"updated_by"`
	UpdatedByName string    `db:"-" json:"updated_by_name"`
	Version       int64     `db:"version" json:"version"`
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

const (
	maxIDLength          = 128
	maxNameLength        = 256
	maxResourceLength    = 256
	maxActionLength      = 256
	maxDescriptionLength = 4096
	maxTreeNodes         = 10000
	maxSortOrder         = 1_000_000_000
)

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
	if err := r.db.SelectContext(ctx, &v, `SELECT `+columns+` FROM permissions WHERE deleted_at IS NULL ORDER BY sort_order,id LIMIT 10001`); err != nil {
		return nil, fmt.Errorf("list permissions: %w", err)
	}
	return v, nil
}

type Service struct {
	repository *Repository
	transactor *database.Transactor
	operations operationlog.TransactionalRecorder
	security   securitylog.TransactionalRecorder
	actors     presentation.ActorResolver
	logger     *slog.Logger
	resources  *pbac.Registry
}

func New(repository *Repository, transactor *database.Transactor, operations operationlog.TransactionalRecorder, security securitylog.TransactionalRecorder, actors presentation.ActorResolver, resources *pbac.Registry, logger *slog.Logger) *Service {
	return &Service{repository: repository, transactor: transactor, operations: operations, security: security, actors: actors, resources: resources, logger: logger}
}
func (s *Service) Get(ctx context.Context, id string) (Record, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxIDLength {
		return Record{}, ErrInvalid
	}
	return s.get(ctx, id)
}

func (s *Service) get(ctx context.Context, id string) (Record, error) {
	record, err := s.repository.Get(ctx, id)
	if err != nil {
		return record, err
	}
	records := []Record{record}
	if err := s.present(ctx, records); err != nil {
		return Record{}, err
	}
	return records[0], nil
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

func (s *Service) Tree(ctx context.Context, input TreeInput) ([]*TreeNode, error) {
	input.Keyword = strings.TrimSpace(input.Keyword)
	if len(input.Keyword) > 256 || len(input.NodeTypes) > 20 || len(input.Statuses) > 20 || !validFilters(input) {
		return nil, ErrInvalid
	}
	records, err := s.repository.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(records) > maxTreeNodes {
		return nil, fmt.Errorf("%w: tree exceeds %d nodes", ErrInvalid, maxTreeNodes)
	}
	records = filterRecords(records, input)
	if err := s.present(ctx, records); err != nil {
		return nil, err
	}
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
	if !validKey.MatchString(input.Key) || input.Name == "" || len(input.Name) > maxNameLength ||
		len(input.Resource) > maxResourceLength || len(input.Action) > maxActionLength || len(input.Description) > maxDescriptionLength ||
		input.SortOrder < -maxSortOrder || input.SortOrder > maxSortOrder || !validPermissionID(input.ParentID) ||
		(input.NodeType != "group" && input.NodeType != "permission") || (input.Status != "active" && input.Status != "disabled") {
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
	input = normalize(input)
	if e = s.validateInput(input); e != nil {
		return Record{}, e
	}
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
	return s.get(ctx, id)
}

// Seed idempotently installs a built-in permission with a stable UUIDv5. It is
// intended only for reviewed deployment manifests, not ordinary CRUD input.
func (s *Service) Seed(ctx context.Context, input Input) (Record, bool, error) {
	if _, err := actor(ctx); err != nil {
		return Record{}, false, err
	}
	input = normalize(input)
	if err := s.validateInput(input); err != nil {
		return Record{}, false, err
	}
	id, err := SeedID(input.Key)
	if err != nil {
		return Record{}, false, err
	}
	var current struct {
		Record
		DeletedAt sql.NullTime `db:"deleted_at"`
	}
	err = s.repository.db.GetContext(ctx, &current, s.repository.db.Rebind(`SELECT `+columns+`,deleted_at FROM permissions WHERE id=?`), id)
	if err == nil && !current.DeletedAt.Valid && current.IsSystem && seedEqual(current.Record, input) {
		records := []Record{current.Record}
		if err := s.present(ctx, records); err != nil {
			return Record{}, false, err
		}
		return records[0], false, nil
	}
	if err == nil && !current.IsSystem {
		return Record{}, false, ErrConflict
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, err
	}
	a, _ := actor(ctx)
	err = s.mutate(ctx, "permission.seed", id, input, func(tx *sqlx.Tx) error {
		treeID := current.ID
		if current.DeletedAt.Valid {
			treeID = ""
		}
		if err := s.validateTreeTx(ctx, tx, treeID, input.ParentID); err != nil {
			return err
		}
		now := time.Now()
		if current.ID == "" {
			_, err := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO permissions (id,parent_id,permission_key,name,node_type,resource,action,description,sort_order,status,is_system,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,?,?,?,?,?,true,?,?,?,?,1)`), id, input.ParentID, input.Key, input.Name, input.NodeType, input.Resource, input.Action, input.Description, input.SortOrder, input.Status, now, a, now, a)
			return err
		}
		result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE permissions SET parent_id=?,permission_key=?,name=?,node_type=?,resource=?,action=?,description=?,sort_order=?,status=?,is_system=true,deleted_at=NULL,deleted_by=NULL,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND is_system=true`), input.ParentID, input.Key, input.Name, input.NodeType, input.Resource, input.Action, input.Description, input.SortOrder, input.Status, now, a, id)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return Record{}, false, err
	}
	record, err := s.repository.Get(ctx, id)
	if err != nil {
		return record, true, err
	}
	records := []Record{record}
	if err := s.present(ctx, records); err != nil {
		return Record{}, true, err
	}
	return records[0], true, nil
}

func SeedID(key string) (string, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	if !validKey.MatchString(key) {
		return "", ErrInvalid
	}
	generator, err := stableid.New(permissionSeedNamespace)
	if err != nil {
		return "", fmt.Errorf("configure permission seed namespace: %w", err)
	}
	id, err := generator.String("permission:" + key)
	if err != nil {
		return "", fmt.Errorf("generate permission seed ID: %w", err)
	}
	return id, nil
}

func seedEqual(record Record, input Input) bool {
	return equalStringPointer(record.ParentID, input.ParentID) && record.Key == input.Key && record.Name == input.Name && record.NodeType == input.NodeType && record.Resource == input.Resource && record.Action == input.Action && record.Description == input.Description && record.SortOrder == input.SortOrder && record.Status == input.Status
}

func equalStringPointer(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func (s *Service) validateTreeTx(ctx context.Context, tx *sqlx.Tx, id string, parent *string) error {
	var records []Record
	e := tx.SelectContext(ctx, &records, `SELECT `+columns+` FROM permissions WHERE deleted_at IS NULL ORDER BY sort_order,id LIMIT 10001`)
	if e != nil {
		return e
	}
	if len(records) > maxTreeNodes {
		return fmt.Errorf("%w: tree exceeds %d nodes", ErrInvalid, maxTreeNodes)
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
	id = strings.TrimSpace(id)
	a, e := actor(ctx)
	if e != nil {
		return Record{}, e
	}
	if id == "" || len(id) > maxIDLength || version <= 0 {
		return Record{}, ErrInvalid
	}
	input = normalize(input)
	if e = s.validateInput(input); e != nil {
		return Record{}, e
	}
	if input.ParentID != nil && *input.ParentID == id {
		return Record{}, ErrInvalid
	}
	e = s.mutate(ctx, "permission.update", id, input, func(tx *sqlx.Tx) error {
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
		if err := s.validateTreeTx(ctx, tx, id, input.ParentID); err != nil {
			return err
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
	return s.get(ctx, id)
}

func (s *Service) validateInput(input Input) error {
	if err := validate(input); err != nil {
		return err
	}
	if input.NodeType != "permission" {
		return nil
	}
	if s == nil || s.resources == nil {
		return ErrInvalid
	}
	if _, _, err := s.resources.Resolve(input.Resource, input.Action); err != nil {
		return fmt.Errorf("%w: resource/action is not registered", ErrInvalid)
	}
	return nil
}
func (s *Service) Delete(ctx context.Context, id string, version int64) error {
	id = strings.TrimSpace(id)
	a, e := actor(ctx)
	if e != nil {
		return e
	}
	if id == "" || len(id) > maxIDLength || version <= 0 {
		return ErrInvalid
	}
	current, e := s.get(ctx, id)
	if e != nil {
		return e
	}
	return s.mutate(ctx, "permission.delete", id, map[string]any{"name": current.Name, "version": version}, func(tx *sqlx.Tx) error {
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

func validPermissionID(id *string) bool {
	return id == nil || (*id != "" && len(*id) <= maxIDLength)
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
func permissionReferenceCount(ctx context.Context, tx *sqlx.Tx, id string) (int, error) {
	queries := []string{
		`SELECT count(*) FROM menus WHERE permission_id=? AND deleted_at IS NULL`,
		`SELECT count(*) FROM tenant_permission_grants WHERE permission_id=? AND deleted_at IS NULL`,
		`SELECT count(*) FROM tenant_role_permissions WHERE permission_id=? AND deleted_at IS NULL`,
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
	started := time.Now()
	resourceName := permissionMutationName(request, id)
	operationEntry := operationlog.Entry{Operation: operation, ResourceType: "permission", ResourceID: id, ResourceName: resourceName, Source: "backend", Protocol: "service", Request: request}
	securityEntry := securitylog.Entry{EventType: securitylog.EventPermissionChanged, SubjectID: id, SubjectName: resourceName, SubjectType: "permission", Metadata: map[string]any{"operation": operation}}
	err := s.transactor.Within(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		operationEntry.Duration = time.Since(started)
		operationEntry.Succeeded = true
		if s.operations != nil {
			if err := s.operations.RecordTx(ctx, tx, operationEntry); err != nil {
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
		err = ErrConflict
	}
	if err != nil {
		operationEntry.Duration = time.Since(started)
		operationEntry.Succeeded = false
		operationEntry.ErrorCode = "operation_failed"
		operationEntry.ErrorMessage = "operation failed"
		if s.operations != nil {
			_ = s.operations.Record(ctx, operationEntry)
		}
		securityEntry.Succeeded = false
		securityEntry.ErrorCode = "operation_failed"
		securityEntry.ErrorMessage = "operation failed"
		if s.security != nil {
			_ = s.security.Record(ctx, securityEntry)
		}
		return err
	}
	return nil
}

func permissionMutationName(request any, fallback string) string {
	switch value := request.(type) {
	case Input:
		if strings.TrimSpace(value.Name) != "" {
			return strings.TrimSpace(value.Name)
		}
	case map[string]any:
		if name, ok := value["name"].(string); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	return fallback
}
