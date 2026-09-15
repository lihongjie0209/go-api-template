package authorization

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
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrTenantAuthorizationInvalid   = errors.New("invalid tenant authorization request")
	ErrTenantAuthorizationForbidden = errors.New("tenant authorization denied")
	ErrTenantAuthorizationNotFound  = errors.New("tenant authorization resource not found")
	ErrTenantAuthorizationConflict  = errors.New("tenant authorization conflict")
	roleCodePattern                 = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,62}$`)
)

type TenantRole struct {
	ID          string    `db:"id" json:"id"`
	TenantID    string    `db:"tenant_id" json:"tenant_id"`
	Code        string    `db:"code" json:"code"`
	Name        string    `db:"name" json:"name"`
	Description string    `db:"description" json:"description"`
	Status      string    `db:"status" json:"status"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	CreatedBy   string    `db:"created_by" json:"created_by"`
	UpdatedAt   time.Time `db:"updated_at" json:"updated_at"`
	UpdatedBy   string    `db:"updated_by" json:"updated_by"`
	Version     int64     `db:"version" json:"version"`
}
type RolePageInput struct {
	pagination.Request
	Keyword       string
	IDs           []string
	Statuses      []string
	CreatedAtFrom *time.Time
	CreatedAtTo   *time.Time
}

// TenantRolePage is the transport documentation shape for a paged role list.
type TenantRolePage struct {
	Items    []TenantRole `json:"items"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
	Total    int64        `json:"total"`
}

type PermissionView struct {
	ID            string `db:"id" json:"id"`
	PermissionKey string `db:"permission_key" json:"permission_key"`
	Name          string `db:"name" json:"name"`
	Resource      string `db:"resource" json:"resource"`
	Action        string `db:"action" json:"action"`
}

type MemberRoleView struct {
	ID   string `db:"id" json:"id"`
	Code string `db:"code" json:"code"`
	Name string `db:"name" json:"name"`
}

type TenantAuthorizationService struct {
	db         *sqlx.DB
	transactor *database.Transactor
	locker     cache.Locker
	operations operationlog.Recorder
	security   securitylog.Recorder
	cfg        config.Config
}

func NewTenantAuthorizationService(db *sqlx.DB, transactor *database.Transactor, locker cache.Locker, operations operationlog.Recorder, security securitylog.Recorder, cfg config.Config) *TenantAuthorizationService {
	return &TenantAuthorizationService{db: db, transactor: transactor, locker: locker, operations: operations, security: security, cfg: cfg}
}

// SetTenantPermissions replaces the tenant's authorization ceiling. Platform
// authorization is enforced by the transport policy; tenant actors cannot call it.
func (s *TenantAuthorizationService) SetTenantPermissions(ctx context.Context, tenantID string, permissionIDs []string) error {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || tenantID == "" || actor.TenantID != "" {
		return ErrTenantAuthorizationForbidden
	}
	permissionIDs, err = normalizeIDs(permissionIDs)
	if err != nil {
		return err
	}
	return s.withLock(ctx, "tenant:"+tenantID+":permissions", func(lockCtx context.Context) error {
		ctx = lockCtx
		return s.logged(ctx, "tenant.permissions.set", tenantID, permissionIDs, func() error {
			return s.transactor.Within(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
				if err := ensureTenant(ctx, tx, tenantID); err != nil {
					return err
				}
				if err := validatePermissions(ctx, tx, permissionIDs); err != nil {
					return err
				}
				if err := replaceLinks(ctx, tx, "tenant_permission_grants", tenantID, "", "permission_id", permissionIDs, actor.ID); err != nil {
					return err
				}
				return pruneRolePermissions(ctx, tx, tenantID, permissionIDs, actor.ID)
			})
		})
	})
}

func (s *TenantAuthorizationService) SetAdministrator(ctx context.Context, tenantID, membershipID string, enabled bool) error {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || tenantID == "" || membershipID == "" {
		return ErrTenantAuthorizationInvalid
	}
	if actor.TenantID != "" && (actor.TenantID != tenantID || !s.isAdministrator(ctx, tenantID, actor.MembershipID)) {
		return ErrTenantAuthorizationForbidden
	}
	return s.withLock(ctx, "tenant:"+tenantID+":administrator:"+membershipID, func(lockCtx context.Context) error {
		ctx = lockCtx
		return s.logged(ctx, "tenant.administrator.set", membershipID, map[string]any{"tenant_id": tenantID, "enabled": enabled}, func() error {
			return s.transactor.Within(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
				if err := ensureMembership(ctx, tx, tenantID, membershipID); err != nil {
					return err
				}
				if enabled {
					return upsertLink(ctx, tx, "tenant_administrators", tenantID, "", "membership_id", membershipID, actor.ID)
				}
				var count int
				query := `SELECT count(*) FROM tenant_administrators a JOIN tenant_memberships m ON m.tenant_id=a.tenant_id AND m.id=a.membership_id AND m.status='active' AND m.deleted_at IS NULL WHERE a.tenant_id=? AND a.deleted_at IS NULL`
				if err := tx.GetContext(ctx, &count, tx.Rebind(query), tenantID); err != nil {
					return err
				}
				if count <= 1 {
					return ErrTenantAuthorizationConflict
				}
				return softDeleteLink(ctx, tx, "tenant_administrators", tenantID, "", "membership_id", membershipID, actor.ID)
			})
		})
	})
}

func (s *TenantAuthorizationService) CreateRole(ctx context.Context, code, name, description string, permissionIDs []string) (TenantRole, error) {
	actor, err := tenantActor(ctx)
	if err != nil {
		return TenantRole{}, err
	}
	code, name = strings.ToLower(strings.TrimSpace(code)), strings.TrimSpace(name)
	permissionIDs, err = normalizeIDs(permissionIDs)
	if err != nil || !roleCodePattern.MatchString(code) || name == "" {
		return TenantRole{}, ErrTenantAuthorizationInvalid
	}
	role := TenantRole{ID: uuid.NewString(), TenantID: actor.TenantID, Code: code, Name: name, Description: description, Status: "active", Version: 1}
	err = s.withLock(ctx, "tenant:"+actor.TenantID+":role-code:"+code, func(lockCtx context.Context) error {
		ctx = lockCtx
		return s.logged(ctx, "tenant.role.create", role.ID, permissionIDs, func() error {
			return s.transactor.Within(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
				if err := ensureAssignable(ctx, tx, actor, permissionIDs); err != nil {
					return err
				}
				now := time.Now()
				_, err := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO tenant_roles (id,tenant_id,code,name,description,status,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,?,?,?,?,?,1)`), role.ID, role.TenantID, role.Code, role.Name, role.Description, role.Status, now, actor.ID, now, actor.ID)
				if err != nil {
					return fmt.Errorf("insert tenant role: %w", err)
				}
				return replaceLinks(ctx, tx, "tenant_role_permissions", actor.TenantID, role.ID, "permission_id", permissionIDs, actor.ID)
			})
		})
	})
	if err != nil {
		return TenantRole{}, err
	}
	return s.GetRole(ctx, role.ID)
}

func (s *TenantAuthorizationService) SetRolePermissions(ctx context.Context, roleID string, version int64, permissionIDs []string) error {
	actor, err := tenantActor(ctx)
	if err != nil || roleID == "" || version <= 0 {
		return ErrTenantAuthorizationInvalid
	}
	permissionIDs, err = normalizeIDs(permissionIDs)
	if err != nil {
		return err
	}
	return s.withLock(ctx, "tenant:"+actor.TenantID+":role:"+roleID, func(lockCtx context.Context) error {
		ctx = lockCtx
		return s.logged(ctx, "tenant.role.permissions.set", roleID, permissionIDs, func() error {
			return s.transactor.Within(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
				if err := ensureAssignable(ctx, tx, actor, permissionIDs); err != nil {
					return err
				}
				result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_roles SET version=version+1,updated_at=?,updated_by=? WHERE tenant_id=? AND id=? AND version=? AND deleted_at IS NULL`), time.Now(), actor.ID, actor.TenantID, roleID, version)
				if err != nil {
					return err
				}
				n, rowsErr := result.RowsAffected()
				if rowsErr != nil {
					return fmt.Errorf("set role permissions affected rows: %w", rowsErr)
				}
				if n != 1 {
					return ErrTenantAuthorizationConflict
				}
				return replaceLinks(ctx, tx, "tenant_role_permissions", actor.TenantID, roleID, "permission_id", permissionIDs, actor.ID)
			})
		})
	})
}

func (s *TenantAuthorizationService) SetMemberRoles(ctx context.Context, membershipID string, roleIDs []string) error {
	actor, err := tenantActor(ctx)
	if err != nil || membershipID == "" {
		return ErrTenantAuthorizationInvalid
	}
	roleIDs, err = normalizeIDs(roleIDs)
	if err != nil {
		return err
	}
	return s.withLock(ctx, "tenant:"+actor.TenantID+":member:"+membershipID+":roles", func(lockCtx context.Context) error {
		ctx = lockCtx
		return s.logged(ctx, "tenant.member.roles.set", membershipID, roleIDs, func() error {
			return s.transactor.Within(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
				if err := ensureMembership(ctx, tx, actor.TenantID, membershipID); err != nil {
					return err
				}
				if err := ensureRolesAssignable(ctx, tx, actor, roleIDs); err != nil {
					return err
				}
				return replaceLinks(ctx, tx, "tenant_member_roles", actor.TenantID, membershipID, "role_id", roleIDs, actor.ID)
			})
		})
	})
}

func (s *TenantAuthorizationService) EffectivePermissions(ctx context.Context, membershipID string) ([]string, error) {
	actor, err := tenantActor(ctx)
	if err != nil {
		return nil, err
	}
	if membershipID == "" {
		membershipID = actor.MembershipID
	}
	if membershipID != actor.MembershipID && !s.isAdministrator(ctx, actor.TenantID, actor.MembershipID) {
		return nil, ErrTenantAuthorizationForbidden
	}
	return effectivePermissionIDs(ctx, s.db, actor.TenantID, membershipID)
}

func (s *TenantAuthorizationService) GetRole(ctx context.Context, roleID string) (TenantRole, error) {
	actor, err := tenantActor(ctx)
	if err != nil {
		return TenantRole{}, err
	}
	var role TenantRole
	err = s.db.GetContext(ctx, &role, s.db.Rebind(`SELECT id,tenant_id,code,name,description,status,created_at,created_by,updated_at,updated_by,version FROM tenant_roles WHERE tenant_id=? AND id=? AND deleted_at IS NULL`), actor.TenantID, roleID)
	if errors.Is(err, sql.ErrNoRows) {
		return role, ErrTenantAuthorizationNotFound
	}
	return role, err
}

func (s *TenantAuthorizationService) PageRoles(ctx context.Context, input RolePageInput) (pagination.Result[TenantRole], error) {
	actor, err := tenantActor(ctx)
	if err != nil {
		return pagination.Result[TenantRole]{}, err
	}
	request, err := pagination.Normalize(input.Request)
	if err != nil || len(input.IDs) > 200 || len(input.Statuses) > 10 || (input.CreatedAtFrom != nil && input.CreatedAtTo != nil && !input.CreatedAtFrom.Before(*input.CreatedAtTo)) {
		return pagination.Result[TenantRole]{}, ErrTenantAuthorizationInvalid
	}
	for _, status := range input.Statuses {
		if status != "active" && status != "disabled" {
			return pagination.Result[TenantRole]{}, ErrTenantAuthorizationInvalid
		}
	}
	where, args := `tenant_id=? AND deleted_at IS NULL`, []any{actor.TenantID}
	if keyword := strings.TrimSpace(input.Keyword); keyword != "" {
		where += ` AND (LOWER(code) LIKE ? OR LOWER(name) LIKE ? OR LOWER(description) LIKE ?)`
		pattern := "%" + strings.ToLower(keyword) + "%"
		args = append(args, pattern, pattern, pattern)
	}
	for _, filter := range []struct {
		column string
		values []string
	}{{"id", input.IDs}, {"status", input.Statuses}} {
		column, values := filter.column, filter.values
		if len(values) == 0 {
			continue
		}
		clause, inArgs, inErr := sqlx.In(column+` IN (?)`, values)
		if inErr != nil {
			return pagination.Result[TenantRole]{}, ErrTenantAuthorizationInvalid
		}
		where += " AND " + clause
		args = append(args, inArgs...)
	}
	if input.CreatedAtFrom != nil {
		where += ` AND created_at>=?`
		args = append(args, *input.CreatedAtFrom)
	}
	if input.CreatedAtTo != nil {
		where += ` AND created_at<?`
		args = append(args, *input.CreatedAtTo)
	}
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*) FROM tenant_roles WHERE `+where), args...); err != nil {
		return pagination.Result[TenantRole]{}, err
	}
	queryArgs := append(append([]any{}, args...), request.PageSize, pagination.Offset(request))
	items := []TenantRole{}
	query := `SELECT id,tenant_id,code,name,description,status,created_at,created_by,updated_at,updated_by,version FROM tenant_roles WHERE ` + where + ` ORDER BY created_at DESC,id LIMIT ? OFFSET ?`
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(query), queryArgs...); err != nil {
		return pagination.Result[TenantRole]{}, err
	}
	return pagination.Result[TenantRole]{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func (s *TenantAuthorizationService) RolePermissions(ctx context.Context, roleID string) ([]PermissionView, error) {
	actor, err := tenantActor(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.GetRole(ctx, roleID); err != nil {
		return nil, err
	}
	permissions := []PermissionView{}
	query := `SELECT p.id,p.permission_key,p.name,p.resource,p.action FROM tenant_role_permissions rp JOIN permissions p ON p.id=rp.permission_id AND p.node_type='permission' AND p.status='active' AND p.deleted_at IS NULL WHERE rp.tenant_id=? AND rp.role_id=? AND rp.deleted_at IS NULL ORDER BY p.permission_key`
	if err := s.db.SelectContext(ctx, &permissions, s.db.Rebind(query), actor.TenantID, roleID); err != nil {
		return nil, err
	}
	return permissions, nil
}

func (s *TenantAuthorizationService) MemberRoles(ctx context.Context, membershipID string) ([]MemberRoleView, error) {
	actor, err := tenantActor(ctx)
	if err != nil {
		return nil, err
	}
	if membershipID == "" {
		return nil, ErrTenantAuthorizationInvalid
	}
	var count int
	if err := s.db.GetContext(ctx, &count, s.db.Rebind(`SELECT count(*) FROM tenant_memberships WHERE tenant_id=? AND id=? AND deleted_at IS NULL`), actor.TenantID, membershipID); err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, ErrTenantAuthorizationNotFound
	}
	roles := []MemberRoleView{}
	query := `SELECT r.id,r.code,r.name FROM tenant_member_roles mr JOIN tenant_roles r ON r.tenant_id=mr.tenant_id AND r.id=mr.role_id AND r.status='active' AND r.deleted_at IS NULL WHERE mr.tenant_id=? AND mr.membership_id=? AND mr.deleted_at IS NULL ORDER BY r.name,r.id`
	if err := s.db.SelectContext(ctx, &roles, s.db.Rebind(query), actor.TenantID, membershipID); err != nil {
		return nil, err
	}
	return roles, nil
}

func (s *TenantAuthorizationService) UpdateRole(ctx context.Context, roleID, name, description, status string, version int64) (TenantRole, error) {
	actor, err := tenantActor(ctx)
	if err != nil {
		return TenantRole{}, err
	}
	name = strings.TrimSpace(name)
	if roleID == "" || name == "" || version <= 0 || (status != "active" && status != "disabled") {
		return TenantRole{}, ErrTenantAuthorizationInvalid
	}
	err = s.withLock(ctx, "tenant:"+actor.TenantID+":role:"+roleID, func(lockCtx context.Context) error {
		ctx = lockCtx
		return s.logged(ctx, "tenant.role.update", roleID, map[string]any{"name": name, "status": status, "version": version}, func() error {
			return s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
				if err := ensureAssignable(ctx, tx, actor, nil); err != nil {
					return err
				}
				result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_roles SET name=?,description=?,status=?,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND id=? AND version=? AND deleted_at IS NULL`), name, description, status, time.Now(), actor.ID, actor.TenantID, roleID, version)
				if err != nil {
					return err
				}
				rows, rowsErr := result.RowsAffected()
				if rowsErr != nil {
					return fmt.Errorf("update tenant role affected rows: %w", rowsErr)
				}
				if rows != 1 {
					return ErrTenantAuthorizationConflict
				}
				return nil
			})
		})
	})
	if err != nil {
		return TenantRole{}, err
	}
	return s.GetRole(ctx, roleID)
}

func (s *TenantAuthorizationService) DeleteRole(ctx context.Context, roleID string, version int64) error {
	actor, err := tenantActor(ctx)
	if err != nil {
		return err
	}
	if roleID == "" || version <= 0 {
		return ErrTenantAuthorizationInvalid
	}
	return s.withLock(ctx, "tenant:"+actor.TenantID+":role:"+roleID, func(lockCtx context.Context) error {
		ctx = lockCtx
		return s.logged(ctx, "tenant.role.delete", roleID, map[string]any{"version": version}, func() error {
			return s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
				if err := ensureAssignable(ctx, tx, actor, nil); err != nil {
					return err
				}
				now := time.Now()
				for _, table := range []string{"tenant_member_roles", "tenant_role_permissions"} {
					if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE `+table+` SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND role_id=? AND deleted_at IS NULL`), now, actor.ID, now, actor.ID, actor.TenantID, roleID); err != nil {
						return err
					}
				}
				result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_roles SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND id=? AND version=? AND deleted_at IS NULL`), now, actor.ID, now, actor.ID, actor.TenantID, roleID, version)
				if err != nil {
					return err
				}
				rows, rowsErr := result.RowsAffected()
				if rowsErr != nil {
					return fmt.Errorf("delete tenant role affected rows: %w", rowsErr)
				}
				if rows != 1 {
					return ErrTenantAuthorizationConflict
				}
				return nil
			})
		})
	})
}

func tenantActor(ctx context.Context) (platformprincipal.Principal, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.TenantID == "" || actor.MembershipID == "" {
		return actor, ErrTenantAuthorizationForbidden
	}
	return actor, nil
}

func normalizeIDs(ids []string) ([]string, error) {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || len(ids) > 1000 {
			return nil, ErrTenantAuthorizationInvalid
		}
		set[id] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

type queryer interface {
	GetContext(context.Context, any, string, ...any) error
	SelectContext(context.Context, any, string, ...any) error
	Rebind(string) string
}

func effectivePermissionIDs(ctx context.Context, db queryer, tenantID, membershipID string) ([]string, error) {
	var active int
	activeQuery := `SELECT count(*) FROM tenant_memberships m JOIN tenants t ON t.id=m.tenant_id AND t.status='active' AND t.deleted_at IS NULL WHERE m.tenant_id=? AND m.id=? AND m.status='active' AND m.deleted_at IS NULL`
	if err := db.GetContext(ctx, &active, db.Rebind(activeQuery), tenantID, membershipID); err != nil {
		return nil, err
	}
	if active != 1 {
		return nil, ErrTenantAuthorizationForbidden
	}
	var admin int
	if err := db.GetContext(ctx, &admin, db.Rebind(`SELECT count(*) FROM tenant_administrators WHERE tenant_id=? AND membership_id=? AND deleted_at IS NULL`), tenantID, membershipID); err != nil {
		return nil, err
	}
	query := `SELECT DISTINCT rp.permission_id FROM tenant_role_permissions rp JOIN tenant_member_roles mr ON mr.tenant_id=rp.tenant_id AND mr.role_id=rp.role_id AND mr.deleted_at IS NULL JOIN tenant_roles r ON r.id=rp.role_id AND r.tenant_id=rp.tenant_id AND r.status='active' AND r.deleted_at IS NULL JOIN tenant_permission_grants g ON g.tenant_id=rp.tenant_id AND g.permission_id=rp.permission_id AND g.deleted_at IS NULL JOIN permissions p ON p.id=rp.permission_id AND p.node_type='permission' AND p.status='active' AND p.deleted_at IS NULL WHERE rp.tenant_id=? AND mr.membership_id=? AND rp.deleted_at IS NULL`
	args := []any{tenantID, membershipID}
	if admin > 0 {
		query = `SELECT g.permission_id FROM tenant_permission_grants g JOIN permissions p ON p.id=g.permission_id AND p.node_type='permission' AND p.status='active' AND p.deleted_at IS NULL WHERE g.tenant_id=? AND g.deleted_at IS NULL`
		args = []any{tenantID}
	}
	ids := []string{}
	if err := db.SelectContext(ctx, &ids, db.Rebind(query), args...); err != nil {
		return nil, err
	}
	sort.Strings(ids)
	return ids, nil
}

func ensureAssignable(ctx context.Context, tx *sqlx.Tx, actor platformprincipal.Principal, wanted []string) error {
	if err := validatePermissions(ctx, tx, wanted); err != nil {
		return err
	}
	effective, err := effectivePermissionIDs(ctx, tx, actor.TenantID, actor.MembershipID)
	if err != nil {
		return err
	}
	return requireSubset(wanted, effective)
}

func ensureRolesAssignable(ctx context.Context, tx *sqlx.Tx, actor platformprincipal.Principal, roleIDs []string) error {
	if len(roleIDs) == 0 {
		return nil
	}
	query, args, inErr := sqlx.In(`SELECT DISTINCT permission_id FROM tenant_role_permissions WHERE tenant_id=? AND role_id IN (?) AND deleted_at IS NULL`, actor.TenantID, roleIDs)
	if inErr != nil {
		return fmt.Errorf("build role permission query: %w", inErr)
	}
	permissions := []string{}
	if err := tx.SelectContext(ctx, &permissions, tx.Rebind(query), args...); err != nil {
		return err
	}
	var roleCount int
	countQuery, countArgs, inErr := sqlx.In(`SELECT count(*) FROM tenant_roles WHERE tenant_id=? AND id IN (?) AND status='active' AND deleted_at IS NULL`, actor.TenantID, roleIDs)
	if inErr != nil {
		return fmt.Errorf("build role validation query: %w", inErr)
	}
	if err := tx.GetContext(ctx, &roleCount, tx.Rebind(countQuery), countArgs...); err != nil || roleCount != len(roleIDs) {
		return ErrTenantAuthorizationInvalid
	}
	return ensureAssignable(ctx, tx, actor, permissions)
}

func requireSubset(wanted, available []string) error {
	set := make(map[string]struct{}, len(available))
	for _, id := range available {
		set[id] = struct{}{}
	}
	for _, id := range wanted {
		if _, ok := set[id]; !ok {
			return ErrTenantAuthorizationForbidden
		}
	}
	return nil
}

func validatePermissions(ctx context.Context, tx *sqlx.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	query, args, inErr := sqlx.In(`SELECT count(*) FROM permissions WHERE id IN (?) AND node_type='permission' AND status='active' AND deleted_at IS NULL`, ids)
	if inErr != nil {
		return fmt.Errorf("build permission validation query: %w", inErr)
	}
	var count int
	if err := tx.GetContext(ctx, &count, tx.Rebind(query), args...); err != nil {
		return err
	}
	if count != len(ids) {
		return ErrTenantAuthorizationInvalid
	}
	return nil
}

func ensureTenant(ctx context.Context, tx *sqlx.Tx, tenantID string) error {
	var count int
	if err := tx.GetContext(ctx, &count, tx.Rebind(`SELECT count(*) FROM tenants WHERE id=? AND status='active' AND deleted_at IS NULL`), tenantID); err != nil {
		return err
	}
	if count != 1 {
		return ErrTenantAuthorizationNotFound
	}
	return nil
}

func ensureMembership(ctx context.Context, tx *sqlx.Tx, tenantID, membershipID string) error {
	var count int
	if err := tx.GetContext(ctx, &count, tx.Rebind(`SELECT count(*) FROM tenant_memberships WHERE tenant_id=? AND id=? AND status='active' AND deleted_at IS NULL`), tenantID, membershipID); err != nil {
		return err
	}
	if count != 1 {
		return ErrTenantAuthorizationNotFound
	}
	return nil
}

func replaceLinks(ctx context.Context, tx *sqlx.Tx, table, tenantID, ownerID, valueColumn string, values []string, actorID string) error {
	now := time.Now()
	where, args := `tenant_id=?`, []any{tenantID}
	if ownerID != "" {
		ownerColumn := "role_id"
		if table == "tenant_member_roles" {
			ownerColumn = "membership_id"
		}
		where += " AND " + ownerColumn + "=?"
		args = append(args, ownerID)
	}
	if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE `+table+` SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE `+where+` AND deleted_at IS NULL`), append([]any{now, actorID, now, actorID}, args...)...); err != nil {
		return err
	}
	for _, value := range values {
		if err := upsertLink(ctx, tx, table, tenantID, ownerID, valueColumn, value, actorID); err != nil {
			return err
		}
	}
	return nil
}

func upsertLink(ctx context.Context, tx *sqlx.Tx, table, tenantID, ownerID, valueColumn, value, actorID string) error {
	ownerColumn := ""
	if ownerID != "" {
		ownerColumn = "role_id"
		if table == "tenant_member_roles" {
			ownerColumn = "membership_id"
		}
	}
	where, args := `tenant_id=? AND `+valueColumn+`=?`, []any{tenantID, value}
	if ownerColumn != "" {
		where, args = `tenant_id=? AND `+ownerColumn+`=? AND `+valueColumn+`=?`, []any{tenantID, ownerID, value}
	}
	var id string
	err := tx.GetContext(ctx, &id, tx.Rebind(`SELECT id FROM `+table+` WHERE `+where), args...)
	now := time.Now()
	if err == nil {
		_, err = tx.ExecContext(ctx, tx.Rebind(`UPDATE `+table+` SET deleted_at=NULL,deleted_by=NULL,updated_at=?,updated_by=?,version=version+1 WHERE id=?`), now, actorID, id)
		return err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	columns, insertArgs, placeholders := "id,tenant_id,", []any{uuid.NewString(), tenantID}, "?,?,"
	if ownerColumn != "" {
		columns += ownerColumn + ","
		insertArgs = append(insertArgs, ownerID)
		placeholders += "?,"
	}
	columns += valueColumn + ",created_at,created_by,updated_at,updated_by,version"
	insertArgs = append(insertArgs, value, now, actorID, now, actorID, 1)
	_, err = tx.ExecContext(ctx, tx.Rebind(`INSERT INTO `+table+` (`+columns+`) VALUES (`+placeholders+`?,?,?,?,?,?)`), insertArgs...)
	return err
}

func softDeleteLink(ctx context.Context, tx *sqlx.Tx, table, tenantID, ownerID, valueColumn, value, actorID string) error {
	_ = ownerID
	now := time.Now()
	result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE `+table+` SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND `+valueColumn+`=? AND deleted_at IS NULL`), now, actorID, now, actorID, tenantID, value)
	if err != nil {
		return err
	}
	n, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("delete authorization link affected rows: %w", rowsErr)
	}
	if n != 1 {
		return ErrTenantAuthorizationNotFound
	}
	return nil
}

func (s *TenantAuthorizationService) isAdministrator(ctx context.Context, tenantID, membershipID string) bool {
	if membershipID == "" || s.db == nil {
		return false
	}
	var count int
	query := `SELECT count(*) FROM tenant_administrators a JOIN tenant_memberships m ON m.tenant_id=a.tenant_id AND m.id=a.membership_id AND m.status='active' AND m.deleted_at IS NULL JOIN tenants t ON t.id=a.tenant_id AND t.status='active' AND t.deleted_at IS NULL WHERE a.tenant_id=? AND a.membership_id=? AND a.deleted_at IS NULL`
	err := s.db.GetContext(ctx, &count, s.db.Rebind(query), tenantID, membershipID)
	return err == nil && count == 1
}

func (s *TenantAuthorizationService) withLock(ctx context.Context, key string, fn func(context.Context) error) error {
	if s.locker == nil {
		return fn(ctx)
	}
	var businessErr error
	err := cache.WithLock(ctx, s.locker, key, s.cfg.User.LockTTL, s.cfg.User.LockRetryDelay, func(lockCtx context.Context) error {
		businessErr = fn(lockCtx)
		return businessErr
	})
	if businessErr != nil {
		return businessErr
	}
	if err != nil {
		return ErrTenantAuthorizationConflict
	}
	return nil
}

func (s *TenantAuthorizationService) logged(ctx context.Context, operation, resourceID string, request any, fn func() error) error {
	committed := false
	err := operationlog.Do(ctx, s.operations, operationlog.Entry{Operation: operation, ResourceType: "tenant_authorization", ResourceID: resourceID, Source: "backend", Protocol: "service", Request: request}, func() error { businessErr := fn(); committed = businessErr == nil; return businessErr })
	actor, _ := platformprincipal.FromContext(ctx)
	tenantID := actor.TenantID
	if tenantID == "" && operation == "tenant.permissions.set" {
		tenantID = resourceID
	}
	if s.security == nil {
		return err
	}
	securityErr := s.security.Record(ctx, securitylog.Entry{EventType: securitylog.EventTenantAuthorization, SubjectID: resourceID, SubjectType: "tenant_authorization", TenantID: tenantID, Succeeded: committed, Metadata: map[string]any{"operation": operation}})
	if securityErr != nil && committed && err == nil && s.security.FailClosed() {
		return securityErr
	}
	return err
}

func pruneRolePermissions(ctx context.Context, tx *sqlx.Tx, tenantID string, allowed []string, actorID string) error {
	where, args := `tenant_id=? AND deleted_at IS NULL`, []any{tenantID}
	if len(allowed) > 0 {
		clause, inArgs, err := sqlx.In(`permission_id NOT IN (?)`, allowed)
		if err != nil {
			return fmt.Errorf("build role permission pruning: %w", err)
		}
		where += " AND " + clause
		args = append(args, inArgs...)
	}
	affectedRoleIDs := []string{}
	if err := tx.SelectContext(ctx, &affectedRoleIDs, tx.Rebind(`SELECT DISTINCT role_id FROM tenant_role_permissions WHERE `+where), args...); err != nil {
		return fmt.Errorf("find roles outside tenant ceiling: %w", err)
	}
	if len(affectedRoleIDs) == 0 {
		return nil
	}
	now := time.Now()
	if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_role_permissions SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE `+where), append([]any{now, actorID, now, actorID}, args...)...); err != nil {
		return fmt.Errorf("prune role permissions outside tenant ceiling: %w", err)
	}
	roleQuery, roleArgs, err := sqlx.In(`UPDATE tenant_roles SET updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND id IN (?) AND deleted_at IS NULL`, now, actorID, tenantID, affectedRoleIDs)
	if err != nil {
		return fmt.Errorf("build affected role version update: %w", err)
	}
	if _, err := tx.ExecContext(ctx, tx.Rebind(roleQuery), roleArgs...); err != nil {
		return fmt.Errorf("update affected role versions: %w", err)
	}
	return nil
}
