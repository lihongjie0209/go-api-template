package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	platformtree "github.com/lihongjie0209/microservice-platform-go/tree"
)

var departmentCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,62}$`)

const (
	maxDepartmentNameLength = 256
	maxDepartmentNodes      = 10000
	maxDepartmentSortOrder  = 1_000_000_000
)

type Department struct {
	ID            string    `db:"id" json:"id"`
	TenantID      string    `db:"tenant_id" json:"tenant_id"`
	ParentID      *string   `db:"parent_id" json:"parent_id"`
	Code          string    `db:"code" json:"code"`
	Name          string    `db:"name" json:"name"`
	SortOrder     int64     `db:"sort_order" json:"sort_order"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
	CreatedBy     string    `db:"created_by" json:"created_by"`
	CreatedByName string    `db:"-" json:"created_by_name"`
	UpdatedAt     time.Time `db:"updated_at" json:"updated_at"`
	UpdatedBy     string    `db:"updated_by" json:"updated_by"`
	UpdatedByName string    `db:"-" json:"updated_by_name"`
	Version       int64     `db:"version" json:"version"`
}
type DepartmentNode struct {
	Department
	Children []*DepartmentNode `json:"children"`
}
type DepartmentInput struct {
	ParentID   *string
	Code, Name string
	SortOrder  int64
}
type DepartmentUpdate struct {
	ID                 string
	ParentID           *string
	Name               string
	SortOrder, Version int64
}
type DepartmentMemberAssignment struct {
	MembershipID string `json:"membership_id" binding:"required"`
	IsPrimary    bool   `json:"is_primary"`
}

type DepartmentService struct {
	db         *sqlx.DB
	tx         *database.Transactor
	locker     cache.Locker
	operations operationlog.TransactionalRecorder
	users      UserResolver
	cfg        config.Config
	dataScopes *datapermission.Service
}

func NewDepartmentService(db *sqlx.DB, tx *database.Transactor, locker cache.Locker, operations operationlog.TransactionalRecorder, users UserResolver, cfg config.Config, dataScopes *datapermission.Service) *DepartmentService {
	return &DepartmentService{db: db, tx: tx, locker: locker, operations: operations, users: users, cfg: cfg, dataScopes: dataScopes}
}

const departmentColumns = `td.id,td.tenant_id,td.parent_id,td.code,td.name,td.sort_order,td.created_at,td.created_by,td.updated_at,td.updated_by,td.version`

func NewDepartmentDataPermissionSchema() *datapermission.Schema {
	schema, err := datapermission.NewSchema("tenant.department", map[string]datapermission.Field{
		"id":         {Column: "td.id", Type: datapermission.ValueTypeText},
		"parent_id":  {Column: "td.parent_id", Type: datapermission.ValueTypeText},
		"code":       {Column: "td.code", Type: datapermission.ValueTypeText},
		"name":       {Column: "td.name", Type: datapermission.ValueTypeText},
		"created_by": {Column: "td.created_by", Type: datapermission.ValueTypeText},
	})
	if err != nil {
		panic(err)
	}
	return schema
}

func (s *DepartmentService) Create(ctx context.Context, input DepartmentInput) (Department, error) {
	actor, err := departmentActor(ctx)
	if err != nil {
		return Department{}, err
	}
	input.Code, input.Name = strings.ToLower(strings.TrimSpace(input.Code)), strings.TrimSpace(input.Name)
	input.ParentID = cleanParent(input.ParentID)
	if !departmentCodePattern.MatchString(input.Code) || input.Name == "" || len(input.Name) > maxDepartmentNameLength || !validDepartmentID(input.ParentID) || input.SortOrder < -maxDepartmentSortOrder || input.SortOrder > maxDepartmentSortOrder {
		return Department{}, ErrInvalid
	}
	record := Department{ID: uuid.NewString(), TenantID: actor.TenantID, ParentID: input.ParentID, Code: input.Code, Name: input.Name, SortOrder: input.SortOrder, Version: 1}
	parentID := ""
	if record.ParentID != nil {
		parentID = *record.ParentID
	}
	if s.dataScopes == nil {
		if actor.Type != platformprincipal.TypeSystem {
			return Department{}, datapermission.ErrScopeRequired
		}
	} else if err := s.dataScopes.AuthorizeObject(ctx, "tenant.department", datapermission.ResourceAttributes{
		"id": record.ID, "parent_id": parentID, "code": record.Code, "name": record.Name, "created_by": actor.ID,
	}); err != nil {
		if errors.Is(err, datapermission.ErrObjectDenied) {
			return Department{}, ErrForbidden
		}
		return Department{}, err
	}
	err = s.withDepartmentLock(ctx, actor.TenantID, "tree", func(lockCtx context.Context) error {
		return s.mutate(lockCtx, "tenant.department.create", record.ID, input, nil, func(tx *sqlx.Tx) error {
			if err := ensureActiveTenant(lockCtx, tx, actor.TenantID); err != nil {
				return err
			}
			if err := ensureDepartmentParent(lockCtx, tx, actor.TenantID, record.ParentID); err != nil {
				return err
			}
			now := time.Now()
			_, err := tx.ExecContext(lockCtx, tx.Rebind(`INSERT INTO tenant_departments (id,tenant_id,parent_id,code,name,sort_order,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,?,?,?,?,?,1)`), record.ID, record.TenantID, record.ParentID, record.Code, record.Name, record.SortOrder, now, actor.ID, now, actor.ID)
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return err
		})
	})
	if err != nil {
		return Department{}, err
	}
	return s.get(ctx, actor.TenantID, record.ID, unrestrictedMemberScope())
}
func (s *DepartmentService) Get(ctx context.Context, id string) (Department, error) {
	actor, err := departmentActor(ctx)
	if err != nil {
		return Department{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxTenantIDLength {
		return Department{}, ErrInvalid
	}
	scope, err := s.departmentScope(ctx)
	if err != nil {
		return Department{}, err
	}
	return s.get(ctx, actor.TenantID, id, scope)
}

func (s *DepartmentService) get(ctx context.Context, tenantID, id string, scope datapermission.SQLPredicate) (Department, error) {
	var record Department
	args := append([]any{tenantID, id}, scope.Args...)
	err := s.db.GetContext(ctx, &record, s.db.Rebind(`SELECT `+departmentColumns+` FROM tenant_departments td WHERE td.tenant_id=? AND td.id=? AND td.deleted_at IS NULL AND `+scope.Clause), args...)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	if err != nil {
		return record, err
	}
	records := []Department{record}
	if err := s.presentDepartments(ctx, records); err != nil {
		return Department{}, err
	}
	return records[0], nil
}
func (s *DepartmentService) Tree(ctx context.Context, keyword string) ([]*DepartmentNode, error) {
	actor, err := departmentActor(ctx)
	if err != nil {
		return nil, err
	}
	keyword = strings.TrimSpace(keyword)
	if len(keyword) > 256 {
		return nil, ErrInvalid
	}
	records := []Department{}
	scope, err := s.departmentScope(ctx)
	if err != nil {
		return nil, err
	}
	query, args := `SELECT `+departmentColumns+` FROM tenant_departments td WHERE td.tenant_id=? AND td.deleted_at IS NULL AND `+scope.Clause, append([]any{actor.TenantID}, scope.Args...)
	query += ` ORDER BY sort_order,id LIMIT 10001`
	if err := s.db.SelectContext(ctx, &records, s.db.Rebind(query), args...); err != nil {
		return nil, err
	}
	if len(records) > maxDepartmentNodes {
		return nil, fmt.Errorf("%w: department tree exceeds %d nodes", ErrInvalid, maxDepartmentNodes)
	}
	records = filterDepartments(records, keyword)
	if err := s.presentDepartments(ctx, records); err != nil {
		return nil, err
	}
	forest, err := platformtree.Build(records, func(v Department) string { return v.ID }, func(v Department) (string, bool) {
		if v.ParentID == nil {
			return "", false
		}
		return *v.ParentID, true
	})
	if err != nil {
		return nil, fmt.Errorf("build department tree: %w", err)
	}
	return mapDepartments(forest), nil
}

func (s *DepartmentService) presentDepartments(ctx context.Context, records []Department) error {
	ids := make([]string, 0, len(records)*2)
	for _, record := range records {
		ids = append(ids, record.CreatedBy, record.UpdatedBy)
	}
	names := stableActorNames(ids)
	if resolver, ok := s.users.(presentation.ActorResolver); ok {
		resolved, err := resolver.ResolveUserIDs(ctx, ids)
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
func filterDepartments(records []Department, keyword string) []Department {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if keyword == "" {
		return records
	}
	byID, keep := make(map[string]Department, len(records)), map[string]struct{}{}
	for _, record := range records {
		byID[record.ID] = record
	}
	for _, record := range records {
		if !strings.Contains(strings.ToLower(record.Code+"\x00"+record.Name), keyword) {
			continue
		}
		current, visited := record, map[string]struct{}{}
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
	filtered := make([]Department, 0, len(keep))
	for _, record := range records {
		if _, ok := keep[record.ID]; ok {
			filtered = append(filtered, record)
		}
	}
	return filtered
}
func mapDepartments(nodes []*platformtree.Node[Department]) []*DepartmentNode {
	out := make([]*DepartmentNode, len(nodes))
	for i, node := range nodes {
		out[i] = &DepartmentNode{Department: node.Value, Children: mapDepartments(node.Children)}
	}
	return out
}

func (s *DepartmentService) Update(ctx context.Context, input DepartmentUpdate) (Department, error) {
	actor, err := departmentActor(ctx)
	if err != nil {
		return Department{}, err
	}
	input.Name, input.ParentID = strings.TrimSpace(input.Name), cleanParent(input.ParentID)
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" || len(input.ID) > maxTenantIDLength || input.Name == "" || len(input.Name) > maxDepartmentNameLength || !validDepartmentID(input.ParentID) || input.SortOrder < -maxDepartmentSortOrder || input.SortOrder > maxDepartmentSortOrder || input.Version <= 0 || (input.ParentID != nil && *input.ParentID == input.ID) {
		return Department{}, ErrInvalid
	}
	scope, err := s.departmentScope(ctx)
	if err != nil {
		return Department{}, err
	}
	current, err := s.get(ctx, actor.TenantID, input.ID, scope)
	if err != nil {
		return Department{}, err
	}
	if s.dataScopes != nil {
		currentParent, proposedParent := "", ""
		if current.ParentID != nil {
			currentParent = *current.ParentID
		}
		if input.ParentID != nil {
			proposedParent = *input.ParentID
		}
		currentAttributes := datapermission.ResourceAttributes{"id": current.ID, "parent_id": currentParent, "code": current.Code, "name": current.Name, "created_by": current.CreatedBy}
		proposedAttributes := datapermission.ResourceAttributes{"id": current.ID, "parent_id": proposedParent, "code": current.Code, "name": input.Name, "created_by": current.CreatedBy}
		if err := s.dataScopes.AuthorizeTransition(ctx, "tenant.department", currentAttributes, proposedAttributes); err != nil {
			if errors.Is(err, datapermission.ErrObjectDenied) {
				return Department{}, ErrForbidden
			}
			return Department{}, err
		}
	}
	err = s.withDepartmentLock(ctx, actor.TenantID, "tree", func(ctx context.Context) error {
		return s.mutate(ctx, "tenant.department.update", input.ID, input, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
			if err := ensureActiveTenant(ctx, tx, actor.TenantID); err != nil {
				return err
			}
			if err := ensureDepartmentMove(ctx, tx, actor.TenantID, input.ID, input.ParentID); err != nil {
				return err
			}
			args := append([]any{input.ParentID, input.Name, input.SortOrder, time.Now(), actor.ID, actor.TenantID, input.ID, input.Version}, scope.Args...)
			result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_departments AS td SET parent_id=?,name=?,sort_order=?,updated_at=?,updated_by=?,version=version+1 WHERE td.tenant_id=? AND td.id=? AND td.version=? AND td.deleted_at IS NULL AND `+scope.Clause), args...)
			if err != nil {
				return err
			}
			rows, rowsErr := result.RowsAffected()
			if rowsErr != nil {
				return fmt.Errorf("update department affected rows: %w", rowsErr)
			}
			if rows != 1 {
				return ErrConflict
			}
			return nil
		})
	})
	if err != nil {
		return Department{}, err
	}
	return s.get(ctx, actor.TenantID, input.ID, unrestrictedMemberScope())
}
func (s *DepartmentService) Delete(ctx context.Context, id string, version int64) error {
	actor, err := departmentActor(ctx)
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxTenantIDLength || version <= 0 {
		return ErrInvalid
	}
	scope, err := s.departmentScope(ctx)
	if err != nil {
		return err
	}
	current, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	return s.withDepartmentLock(ctx, actor.TenantID, "tree", func(ctx context.Context) error {
		return s.mutate(ctx, "tenant.department.delete", id, map[string]any{"name": current.Name, "version": version}, nil, func(tx *sqlx.Tx) error {
			if err := ensureActiveTenant(ctx, tx, actor.TenantID); err != nil {
				return err
			}
			var dependents int
			q := `SELECT (SELECT count(*) FROM tenant_departments WHERE tenant_id=? AND parent_id=? AND deleted_at IS NULL)+(SELECT count(*) FROM tenant_department_members WHERE tenant_id=? AND department_id=? AND deleted_at IS NULL)`
			if err := tx.GetContext(ctx, &dependents, tx.Rebind(q), actor.TenantID, id, actor.TenantID, id); err != nil {
				return err
			}
			if dependents > 0 {
				return ErrConflict
			}
			now := time.Now()
			args := append([]any{now, actor.ID, now, actor.ID, actor.TenantID, id, version}, scope.Args...)
			result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_departments AS td SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE td.tenant_id=? AND td.id=? AND td.version=? AND td.deleted_at IS NULL AND `+scope.Clause), args...)
			if err != nil {
				return err
			}
			rows, rowsErr := result.RowsAffected()
			if rowsErr != nil {
				return fmt.Errorf("delete department affected rows: %w", rowsErr)
			}
			if rows != 1 {
				return ErrConflict
			}
			return nil
		})
	})
}
func (s *DepartmentService) SetMembers(ctx context.Context, departmentID string, assignments []DepartmentMemberAssignment) error {
	actor, err := departmentActor(ctx)
	if err != nil {
		return err
	}
	departmentID = strings.TrimSpace(departmentID)
	if departmentID == "" || len(departmentID) > maxTenantIDLength || len(assignments) > 1000 {
		return ErrInvalid
	}
	seen := make(map[string]struct{}, len(assignments))
	ids := make([]string, len(assignments))
	for i, assignment := range assignments {
		assignment.MembershipID = strings.TrimSpace(assignment.MembershipID)
		if assignment.MembershipID == "" || len(assignment.MembershipID) > maxTenantIDLength {
			return ErrInvalid
		}
		assignments[i].MembershipID = assignment.MembershipID
		if _, exists := seen[assignment.MembershipID]; exists {
			return ErrInvalid
		}
		seen[assignment.MembershipID] = struct{}{}
		ids[i] = assignment.MembershipID
	}
	scope, err := s.departmentScope(ctx)
	if err != nil {
		return err
	}
	return s.withDepartmentLock(ctx, actor.TenantID, "members", func(ctx context.Context) error {
		primaryCount := 0
		for _, assignment := range assignments {
			if assignment.IsPrimary {
				primaryCount++
			}
		}
		request := map[string]any{"assignment_count": len(assignments), "primary_count": primaryCount}
		return s.mutate(ctx, "tenant.department.members.set", departmentID, request, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
			if err := ensureActiveTenant(ctx, tx, actor.TenantID); err != nil {
				return err
			}
			var departmentName string
			query := tx.Rebind(`SELECT td.name FROM tenant_departments td WHERE td.tenant_id=? AND td.id=? AND td.deleted_at IS NULL AND ` + scope.Clause)
			args := append([]any{actor.TenantID, departmentID}, scope.Args...)
			if err := tx.GetContext(ctx, &departmentName, query, args...); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrNotFound
				}
				return err
			}
			request["name"] = departmentName
			if err := ensureDepartmentParent(ctx, tx, actor.TenantID, &departmentID); err != nil {
				return err
			}
			if len(ids) > 0 {
				query, args, inErr := sqlx.In(`SELECT count(*) FROM tenant_memberships WHERE tenant_id=? AND id IN (?) AND status='active' AND deleted_at IS NULL`, actor.TenantID, ids)
				if inErr != nil {
					return fmt.Errorf("build department member validation: %w", inErr)
				}
				var count int
				if err := tx.GetContext(ctx, &count, tx.Rebind(query), args...); err != nil {
					return err
				}
				if count != len(ids) {
					return ErrInvalid
				}
			}
			now := time.Now()
			if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_department_members SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND department_id=? AND deleted_at IS NULL`), now, actor.ID, now, actor.ID, actor.TenantID, departmentID); err != nil {
				return err
			}
			for _, assignment := range assignments {
				if assignment.IsPrimary {
					if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_department_members SET is_primary=false,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND membership_id=? AND is_primary=true AND deleted_at IS NULL`), now, actor.ID, actor.TenantID, assignment.MembershipID); err != nil {
						return err
					}
				}
				var linkID string
				findErr := tx.GetContext(ctx, &linkID, tx.Rebind(`SELECT id FROM tenant_department_members WHERE tenant_id=? AND department_id=? AND membership_id=?`), actor.TenantID, departmentID, assignment.MembershipID)
				switch {
				case findErr == nil:
					_, err = tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_department_members SET is_primary=?,deleted_at=NULL,deleted_by=NULL,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND id=?`), assignment.IsPrimary, now, actor.ID, actor.TenantID, linkID)
				case errors.Is(findErr, sql.ErrNoRows):
					_, err = tx.ExecContext(ctx, tx.Rebind(`INSERT INTO tenant_department_members(id,tenant_id,department_id,membership_id,is_primary,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,1)`), uuid.NewString(), actor.TenantID, departmentID, assignment.MembershipID, assignment.IsPrimary, now, actor.ID, now, actor.ID)
				default:
					err = findErr
				}
				if err != nil {
					if isUniqueViolation(err) {
						return ErrConflict
					}
					return err
				}
			}
			return nil
		})
	})
}

func (s *DepartmentService) departmentScope(ctx context.Context) (datapermission.SQLPredicate, error) {
	if s.dataScopes == nil {
		actor, err := platformprincipal.Require(ctx)
		if err == nil && actor.Type == platformprincipal.TypeSystem {
			return unrestrictedMemberScope(), nil
		}
		return datapermission.SQLPredicate{}, datapermission.ErrScopeRequired
	}
	return s.dataScopes.Compile(ctx, "tenant.department")
}

func (s *DepartmentService) mutate(ctx context.Context, operation, id string, request any, options *sql.TxOptions, fn func(*sqlx.Tx) error) error {
	started := time.Now()
	entry := operationlog.Entry{Operation: operation, ResourceType: "tenant_department", ResourceID: id, ResourceName: departmentMutationName(request, id), Source: "backend", Protocol: "service", Request: request}
	err := s.tx.Within(ctx, options, func(tx *sqlx.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		entry.ResourceName = departmentMutationName(request, id)
		entry.Duration = time.Since(started)
		entry.Succeeded = true
		if s.operations != nil {
			return s.operations.RecordTx(ctx, tx, entry)
		}
		return nil
	})
	if err != nil && s.operations != nil {
		entry.ResourceName = departmentMutationName(request, id)
		entry.Duration = time.Since(started)
		entry.Succeeded = false
		entry.ErrorCode = "operation_failed"
		entry.ErrorMessage = "operation failed"
		_ = s.operations.Record(ctx, entry)
	}
	return err
}

func departmentMutationName(request any, fallback string) string {
	switch value := request.(type) {
	case DepartmentInput:
		return value.Name
	case DepartmentUpdate:
		return value.Name
	case map[string]any:
		if name, ok := value["name"].(string); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	return fallback
}
func departmentActor(ctx context.Context) (platformprincipal.Principal, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.TenantID == "" || actor.MembershipID == "" {
		return actor, ErrForbidden
	}
	return actor, nil
}
func cleanParent(parent *string) *string {
	if parent == nil {
		return nil
	}
	value := strings.TrimSpace(*parent)
	if value == "" {
		return nil
	}
	return &value
}

func validDepartmentID(id *string) bool {
	return id == nil || (len(*id) > 0 && len(*id) <= maxTenantIDLength)
}
func ensureDepartmentParent(ctx context.Context, tx *sqlx.Tx, tenantID string, parent *string) error {
	if parent == nil {
		return nil
	}
	var count int
	if err := tx.GetContext(ctx, &count, tx.Rebind(`SELECT count(*) FROM tenant_departments WHERE tenant_id=? AND id=? AND deleted_at IS NULL`), tenantID, *parent); err != nil {
		return err
	}
	if count != 1 {
		return ErrInvalid
	}
	return nil
}
func ensureDepartmentMove(ctx context.Context, tx *sqlx.Tx, tenantID, id string, parent *string) error {
	if err := ensureDepartmentParent(ctx, tx, tenantID, parent); err != nil {
		return err
	}
	if parent == nil {
		return nil
	}
	var records []struct {
		ID       string  `db:"id"`
		ParentID *string `db:"parent_id"`
	}
	if err := tx.SelectContext(ctx, &records, tx.Rebind(`SELECT id,parent_id FROM tenant_departments WHERE tenant_id=? AND deleted_at IS NULL`), tenantID); err != nil {
		return err
	}
	parents := make(map[string]*string, len(records))
	for _, record := range records {
		parents[record.ID] = record.ParentID
	}
	return validateDepartmentMove(id, *parent, parents)
}
func validateDepartmentMove(id, parent string, parents map[string]*string) error {
	visited := map[string]struct{}{}
	for current := parent; current != ""; {
		if current == id {
			return ErrInvalid
		}
		if _, exists := visited[current]; exists {
			return ErrInvalid
		}
		visited[current] = struct{}{}
		next := parents[current]
		if next == nil {
			return nil
		}
		current = *next
	}
	return nil
}
func (s *DepartmentService) withDepartmentLock(ctx context.Context, tenantID, suffix string, fn func(context.Context) error) error {
	if s.locker == nil {
		return fn(ctx)
	}
	var businessErr error
	err := cache.WithLock(ctx, s.locker, "tenant:"+tenantID+":department:"+suffix, s.cfg.DistributedLock.TTL, s.cfg.DistributedLock.RetryDelay, func(lockCtx context.Context) error {
		businessErr = fn(lockCtx)
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
