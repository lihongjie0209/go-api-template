package tenant

import (
	"context"
	"database/sql"
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
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	platformtree "github.com/lihongjie0209/microservice-platform-go/tree"
)

var departmentCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,62}$`)

type Department struct {
	ID        string    `db:"id" json:"id"`
	TenantID  string    `db:"tenant_id" json:"tenant_id"`
	ParentID  *string   `db:"parent_id" json:"parent_id"`
	Code      string    `db:"code" json:"code"`
	Name      string    `db:"name" json:"name"`
	SortOrder int64     `db:"sort_order" json:"sort_order"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	CreatedBy string    `db:"created_by" json:"created_by"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
	UpdatedBy string    `db:"updated_by" json:"updated_by"`
	Version   int64     `db:"version" json:"version"`
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
	operations operationlog.Recorder
	cfg        config.Config
}

func NewDepartmentService(db *sqlx.DB, tx *database.Transactor, locker cache.Locker, operations operationlog.Recorder, cfg config.Config) *DepartmentService {
	return &DepartmentService{db, tx, locker, operations, cfg}
}

const departmentColumns = `id,tenant_id,parent_id,code,name,sort_order,created_at,created_by,updated_at,updated_by,version`

func (s *DepartmentService) Create(ctx context.Context, input DepartmentInput) (Department, error) {
	actor, err := departmentActor(ctx)
	if err != nil {
		return Department{}, err
	}
	input.Code, input.Name = strings.ToLower(strings.TrimSpace(input.Code)), strings.TrimSpace(input.Name)
	if !departmentCodePattern.MatchString(input.Code) || input.Name == "" {
		return Department{}, ErrInvalid
	}
	record := Department{ID: uuid.NewString(), TenantID: actor.TenantID, ParentID: cleanParent(input.ParentID), Code: input.Code, Name: input.Name, SortOrder: input.SortOrder, Version: 1}
	err = s.withDepartmentLock(ctx, actor.TenantID, "tree", func(lockCtx context.Context) error {
		return operationlog.Do(lockCtx, s.operations, operationlog.Entry{Operation: "tenant.department.create", ResourceType: "tenant_department", ResourceID: record.ID, Source: "backend", Protocol: "service", Request: input}, func() error {
			return s.tx.Within(lockCtx, nil, func(tx *sqlx.Tx) error {
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
	})
	if err != nil {
		return Department{}, err
	}
	return s.Get(ctx, record.ID)
}
func (s *DepartmentService) Get(ctx context.Context, id string) (Department, error) {
	actor, err := departmentActor(ctx)
	if err != nil {
		return Department{}, err
	}
	if id == "" {
		return Department{}, ErrInvalid
	}
	var record Department
	err = s.db.GetContext(ctx, &record, s.db.Rebind(`SELECT `+departmentColumns+` FROM tenant_departments WHERE tenant_id=? AND id=? AND deleted_at IS NULL`), actor.TenantID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	return record, err
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
	query, args := `SELECT `+departmentColumns+` FROM tenant_departments WHERE tenant_id=? AND deleted_at IS NULL`, []any{actor.TenantID}
	query += ` ORDER BY sort_order,id`
	if err := s.db.SelectContext(ctx, &records, s.db.Rebind(query), args...); err != nil {
		return nil, err
	}
	if len(records) > 10000 {
		return nil, fmt.Errorf("%w: department tree exceeds 10000 nodes", ErrInvalid)
	}
	records = filterDepartments(records, keyword)
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
	if input.ID == "" || input.Name == "" || input.Version <= 0 || (input.ParentID != nil && *input.ParentID == input.ID) {
		return Department{}, ErrInvalid
	}
	err = s.withDepartmentLock(ctx, actor.TenantID, "tree", func(lockCtx context.Context) error {
		ctx = lockCtx
		return operationlog.Do(ctx, s.operations, operationlog.Entry{Operation: "tenant.department.update", ResourceType: "tenant_department", ResourceID: input.ID, Source: "backend", Protocol: "service", Request: input}, func() error {
			return s.tx.Within(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
				if err := ensureActiveTenant(ctx, tx, actor.TenantID); err != nil {
					return err
				}
				if err := ensureDepartmentMove(ctx, tx, actor.TenantID, input.ID, input.ParentID); err != nil {
					return err
				}
				result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_departments SET parent_id=?,name=?,sort_order=?,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND id=? AND version=? AND deleted_at IS NULL`), input.ParentID, input.Name, input.SortOrder, time.Now(), actor.ID, actor.TenantID, input.ID, input.Version)
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
	})
	if err != nil {
		return Department{}, err
	}
	return s.Get(ctx, input.ID)
}
func (s *DepartmentService) Delete(ctx context.Context, id string, version int64) error {
	actor, err := departmentActor(ctx)
	if err != nil {
		return err
	}
	if id == "" || version <= 0 {
		return ErrInvalid
	}
	return s.withDepartmentLock(ctx, actor.TenantID, "tree", func(lockCtx context.Context) error {
		ctx = lockCtx
		return operationlog.Do(ctx, s.operations, operationlog.Entry{Operation: "tenant.department.delete", ResourceType: "tenant_department", ResourceID: id, Source: "backend", Protocol: "service"}, func() error {
			return s.tx.Within(ctx, nil, func(tx *sqlx.Tx) error {
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
				result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_departments SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND id=? AND version=? AND deleted_at IS NULL`), now, actor.ID, now, actor.ID, actor.TenantID, id, version)
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
	})
}
func (s *DepartmentService) SetMembers(ctx context.Context, departmentID string, assignments []DepartmentMemberAssignment) error {
	actor, err := departmentActor(ctx)
	if err != nil {
		return err
	}
	if departmentID == "" || len(assignments) > 1000 {
		return ErrInvalid
	}
	seen := make(map[string]struct{}, len(assignments))
	ids := make([]string, len(assignments))
	for i, assignment := range assignments {
		if assignment.MembershipID == "" {
			return ErrInvalid
		}
		if _, exists := seen[assignment.MembershipID]; exists {
			return ErrInvalid
		}
		seen[assignment.MembershipID] = struct{}{}
		ids[i] = assignment.MembershipID
	}
	return s.withDepartmentLock(ctx, actor.TenantID, departmentID+":members", func(lockCtx context.Context) error {
		ctx = lockCtx
		return operationlog.Do(ctx, s.operations, operationlog.Entry{Operation: "tenant.department.members.set", ResourceType: "tenant_department", ResourceID: departmentID, Source: "backend", Protocol: "service", Request: assignments}, func() error {
			return s.tx.Within(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
				if err := ensureActiveTenant(ctx, tx, actor.TenantID); err != nil {
					return err
				}
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
						_, err = tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_department_members SET is_primary=?,deleted_at=NULL,deleted_by=NULL,updated_at=?,updated_by=?,version=version+1 WHERE id=?`), assignment.IsPrimary, now, actor.ID, linkID)
					case errors.Is(findErr, sql.ErrNoRows):
						_, err = tx.ExecContext(ctx, tx.Rebind(`INSERT INTO tenant_department_members(id,tenant_id,department_id,membership_id,is_primary,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,1)`), uuid.NewString(), actor.TenantID, departmentID, assignment.MembershipID, assignment.IsPrimary, now, actor.ID, now, actor.ID)
					default:
						err = findErr
					}
					if err != nil {
						return err
					}
				}
				return nil
			})
		})
	})
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
	err := cache.WithLock(ctx, s.locker, "tenant:"+tenantID+":department:"+suffix, s.cfg.User.LockTTL, s.cfg.User.LockRetryDelay, func(lockCtx context.Context) error {
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

func sortDepartmentIDs(values []string) []string { sort.Strings(values); return values }
