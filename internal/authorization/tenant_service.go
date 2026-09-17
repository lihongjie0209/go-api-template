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
	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/lihongjie0209/go-api-template/internal/presentation"
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

const (
	maxAuthorizationIDLength = 128
	maxRoleNameLength        = 256
	maxRoleDescriptionLength = 4096
	maxRoleKeywordLength     = 256
)

type TenantRole struct {
	ID            string    `db:"id" json:"id"`
	TenantID      string    `db:"tenant_id" json:"tenant_id"`
	Code          string    `db:"code" json:"code"`
	Name          string    `db:"name" json:"name"`
	Description   string    `db:"description" json:"description"`
	Status        string    `db:"status" json:"status"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
	CreatedBy     string    `db:"created_by" json:"created_by"`
	CreatedByName string    `db:"-" json:"created_by_name"`
	UpdatedAt     time.Time `db:"updated_at" json:"updated_at"`
	UpdatedBy     string    `db:"updated_by" json:"updated_by"`
	UpdatedByName string    `db:"-" json:"updated_by_name"`
	Version       int64     `db:"version" json:"version"`
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
	operations operationlog.TransactionalRecorder
	security   securitylog.TransactionalRecorder
	actors     presentation.ActorResolver
	cfg        config.Config
	dataScopes *datapermission.Service
	registry   *pbac.Registry
}

func NewTenantAuthorizationService(db *sqlx.DB, transactor *database.Transactor, locker cache.Locker, operations operationlog.TransactionalRecorder, security securitylog.TransactionalRecorder, actors presentation.ActorResolver, cfg config.Config, dataScopes *datapermission.Service, registry *pbac.Registry) *TenantAuthorizationService {
	return &TenantAuthorizationService{db: db, transactor: transactor, locker: locker, operations: operations, security: security, actors: actors, cfg: cfg, dataScopes: dataScopes, registry: registry}
}

func NewTenantRoleDataPermissionSchema() *datapermission.Schema {
	schema, err := datapermission.NewSchema("tenant.role", map[string]datapermission.Field{
		"id":         {Column: "tr.id", Type: datapermission.ValueTypeText},
		"code":       {Column: "tr.code", Type: datapermission.ValueTypeText},
		"name":       {Column: "tr.name", Type: datapermission.ValueTypeText},
		"status":     {Column: "tr.status", Type: datapermission.ValueTypeText},
		"created_by": {Column: "tr.created_by", Type: datapermission.ValueTypeText},
	})
	if err != nil {
		panic(err)
	}
	return schema
}

// SetTenantPermissions replaces the tenant's authorization ceiling. Platform
// authorization is enforced by the transport policy; tenant actors cannot call it.
func (s *TenantAuthorizationService) SetTenantPermissions(ctx context.Context, tenantID string, version int64, permissionIDs []string) error {
	actor, err := platformprincipal.Require(ctx)
	tenantID = strings.TrimSpace(tenantID)
	if err != nil || actor.TenantID != "" {
		return ErrTenantAuthorizationForbidden
	}
	if tenantID == "" || len(tenantID) > maxAuthorizationIDLength || version <= 0 {
		return ErrTenantAuthorizationInvalid
	}
	permissionIDs, err = normalizeIDs(permissionIDs)
	if err != nil {
		return err
	}
	tenantName, err := s.tenantName(ctx, tenantID)
	if err != nil {
		return err
	}
	return s.withLock(ctx, "tenant:"+tenantID+":permissions", func(ctx context.Context) error {
		return s.mutate(ctx, "tenant.permissions.set", tenantID, map[string]any{"name": tenantName, "permission_ids": permissionIDs}, tenantID, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
			if err := ensureTenant(ctx, tx, tenantID); err != nil {
				return err
			}
			if err := s.validateTenantPermissions(ctx, tx, permissionIDs); err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenants SET updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`), time.Now(), actor.ID, tenantID, version)
			if err != nil {
				return err
			}
			rows, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("set tenant permissions affected rows: %w", err)
			}
			if rows != 1 {
				return ErrTenantAuthorizationConflict
			}
			if err := replaceLinks(ctx, tx, "tenant_permission_grants", tenantID, "", "permission_id", permissionIDs, actor.ID); err != nil {
				return err
			}
			return pruneRolePermissions(ctx, tx, tenantID, permissionIDs, actor.ID)
		})
	})
}

func (s *TenantAuthorizationService) SetAdministrator(ctx context.Context, tenantID, membershipID string, enabled bool) error {
	actor, err := platformprincipal.Require(ctx)
	tenantID, membershipID = strings.TrimSpace(tenantID), strings.TrimSpace(membershipID)
	if err != nil || tenantID == "" || membershipID == "" || len(tenantID) > maxAuthorizationIDLength || len(membershipID) > maxAuthorizationIDLength {
		return ErrTenantAuthorizationInvalid
	}
	if actor.TenantID != "" && (actor.TenantID != tenantID || !s.isAdministrator(ctx, tenantID, actor.MembershipID)) {
		return ErrTenantAuthorizationForbidden
	}
	memberName, err := s.membershipName(ctx, tenantID, membershipID)
	if err != nil {
		return err
	}
	return s.withLock(ctx, "tenant:"+tenantID+":administrators", func(ctx context.Context) error {
		return s.mutate(ctx, "tenant.administrator.set", membershipID, map[string]any{"name": memberName, "tenant_id": tenantID, "enabled": enabled}, tenantID, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
			if actor.TenantID != "" {
				if err := ensureAdministrator(ctx, tx, tenantID, actor.MembershipID); err != nil {
					return err
				}
			}
			if err := ensureMembership(ctx, tx, tenantID, membershipID); err != nil {
				return err
			}
			if enabled {
				return upsertLink(ctx, tx, "tenant_administrators", tenantID, "", "membership_id", membershipID, actor.ID)
			}
			if err := lockTenant(ctx, tx, tenantID); err != nil {
				return err
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
}

func (s *TenantAuthorizationService) CreateRole(ctx context.Context, code, name, description string, permissionIDs []string) (TenantRole, error) {
	actor, err := tenantActor(ctx)
	if err != nil {
		return TenantRole{}, err
	}
	code, name, description = strings.ToLower(strings.TrimSpace(code)), strings.TrimSpace(name), strings.TrimSpace(description)
	permissionIDs, err = normalizeIDs(permissionIDs)
	if err != nil || !roleCodePattern.MatchString(code) || name == "" || len(name) > maxRoleNameLength || len(description) > maxRoleDescriptionLength {
		return TenantRole{}, ErrTenantAuthorizationInvalid
	}
	role := TenantRole{ID: uuid.NewString(), TenantID: actor.TenantID, Code: code, Name: name, Description: description, Status: "active", Version: 1}
	if s.dataScopes == nil {
		if actor.Type != platformprincipal.TypeSystem {
			return TenantRole{}, datapermission.ErrScopeRequired
		}
	} else if err := s.dataScopes.AuthorizeObject(ctx, "tenant.role", datapermission.ResourceAttributes{
		"id": role.ID, "code": role.Code, "name": role.Name, "status": role.Status, "created_by": actor.ID,
	}); err != nil {
		if errors.Is(err, datapermission.ErrObjectDenied) {
			return TenantRole{}, ErrTenantAuthorizationForbidden
		}
		return TenantRole{}, err
	}
	err = s.withLock(ctx, "tenant:"+actor.TenantID+":role-code:"+code, func(ctx context.Context) error {
		return s.mutate(ctx, "tenant.role.create", role.ID, map[string]any{"name": role.Name, "permission_ids": permissionIDs}, actor.TenantID, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
			if err := s.ensureAssignable(ctx, tx, actor, permissionIDs); err != nil {
				return err
			}
			now := time.Now()
			_, err := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO tenant_roles (id,tenant_id,code,name,description,status,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,?,?,?,?,?,1)`), role.ID, role.TenantID, role.Code, role.Name, role.Description, role.Status, now, actor.ID, now, actor.ID)
			if err != nil {
				if database.IsUniqueViolation(err) {
					return ErrTenantAuthorizationConflict
				}
				return fmt.Errorf("insert tenant role: %w", err)
			}
			return replaceLinks(ctx, tx, "tenant_role_permissions", actor.TenantID, role.ID, "permission_id", permissionIDs, actor.ID)
		})
	})
	if err != nil {
		return TenantRole{}, err
	}
	return s.getRole(ctx, actor.TenantID, role.ID, unrestrictedScope())
}

func (s *TenantAuthorizationService) SetRolePermissions(ctx context.Context, roleID string, version int64, permissionIDs []string) error {
	actor, err := tenantActor(ctx)
	roleID = strings.TrimSpace(roleID)
	if err != nil || roleID == "" || len(roleID) > maxAuthorizationIDLength || version <= 0 {
		return ErrTenantAuthorizationInvalid
	}
	permissionIDs, err = normalizeIDs(permissionIDs)
	if err != nil {
		return err
	}
	scope, err := s.roleScope(ctx)
	if err != nil {
		return err
	}
	current, err := s.getRole(ctx, actor.TenantID, roleID, scope)
	if err != nil {
		return err
	}
	roleName := current.Name
	return s.withLock(ctx, "tenant:"+actor.TenantID+":role:"+roleID, func(ctx context.Context) error {
		return s.mutate(ctx, "tenant.role.permissions.set", roleID, map[string]any{"name": roleName, "permission_ids": permissionIDs}, actor.TenantID, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
			if err := s.ensureRoleAssignable(ctx, tx, actor, roleID); err != nil {
				return err
			}
			if err := s.ensureAssignable(ctx, tx, actor, permissionIDs); err != nil {
				return err
			}
			args := append([]any{time.Now(), actor.ID, actor.TenantID, roleID, version}, scope.Args...)
			result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_roles AS tr SET version=version+1,updated_at=?,updated_by=? WHERE tr.tenant_id=? AND tr.id=? AND tr.version=? AND tr.deleted_at IS NULL AND `+scope.Clause), args...)
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
}

func (s *TenantAuthorizationService) SetMemberRoles(ctx context.Context, membershipID string, version int64, roleIDs []string) error {
	actor, err := tenantActor(ctx)
	membershipID = strings.TrimSpace(membershipID)
	if err != nil || membershipID == "" || len(membershipID) > maxAuthorizationIDLength || version <= 0 {
		return ErrTenantAuthorizationInvalid
	}
	roleIDs, err = normalizeIDs(roleIDs)
	if err != nil {
		return err
	}
	scope, err := s.memberScope(ctx)
	if err != nil {
		return err
	}
	memberName, err := s.scopedMembershipName(ctx, actor.TenantID, membershipID, scope)
	if err != nil {
		return err
	}
	return s.withLock(ctx, "tenant:"+actor.TenantID+":member:"+membershipID+":roles", func(ctx context.Context) error {
		return s.mutate(ctx, "tenant.member.roles.set", membershipID, map[string]any{"name": memberName, "role_ids": roleIDs}, actor.TenantID, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
			if err := ensureMembership(ctx, tx, actor.TenantID, membershipID); err != nil {
				return err
			}
			if err := s.ensureRolesAssignable(ctx, tx, actor, roleIDs); err != nil {
				return err
			}
			args := append([]any{time.Now(), actor.ID, actor.TenantID, membershipID, version}, scope.Args...)
			result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_memberships AS tm SET updated_at=?,updated_by=?,version=version+1 WHERE tm.tenant_id=? AND tm.id=? AND tm.version=? AND tm.status='active' AND tm.deleted_at IS NULL AND `+scope.Clause), args...)
			if err != nil {
				return err
			}
			rows, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("set member roles affected rows: %w", err)
			}
			if rows != 1 {
				return ErrTenantAuthorizationConflict
			}
			return replaceLinks(ctx, tx, "tenant_member_roles", actor.TenantID, membershipID, "role_id", roleIDs, actor.ID)
		})
	})
}

func (s *TenantAuthorizationService) EffectivePermissions(ctx context.Context, membershipID string) ([]string, error) {
	actor, err := tenantActor(ctx)
	if err != nil {
		return nil, err
	}
	membershipID = strings.TrimSpace(membershipID)
	if membershipID == "" {
		membershipID = actor.MembershipID
	}
	if len(membershipID) > maxAuthorizationIDLength {
		return nil, ErrTenantAuthorizationInvalid
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
	roleID = strings.TrimSpace(roleID)
	if roleID == "" || len(roleID) > maxAuthorizationIDLength {
		return TenantRole{}, ErrTenantAuthorizationInvalid
	}
	scope, err := s.roleScope(ctx)
	if err != nil {
		return TenantRole{}, err
	}
	return s.getRole(ctx, actor.TenantID, roleID, scope)
}

func (s *TenantAuthorizationService) getRole(ctx context.Context, tenantID, roleID string, scope datapermission.SQLPredicate) (TenantRole, error) {
	var role TenantRole
	args := append([]any{tenantID, roleID}, scope.Args...)
	err := s.db.GetContext(ctx, &role, s.db.Rebind(`SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status,tr.created_at,tr.created_by,tr.updated_at,tr.updated_by,tr.version FROM tenant_roles tr WHERE tr.tenant_id=? AND tr.id=? AND tr.deleted_at IS NULL AND `+scope.Clause), args...)
	if errors.Is(err, sql.ErrNoRows) {
		return role, ErrTenantAuthorizationNotFound
	}
	if err != nil {
		return role, err
	}
	roles := []TenantRole{role}
	if err := s.presentRoles(ctx, roles); err != nil {
		return TenantRole{}, err
	}
	return roles[0], nil
}

func (s *TenantAuthorizationService) PageRoles(ctx context.Context, input RolePageInput) (pagination.Result[TenantRole], error) {
	actor, err := tenantActor(ctx)
	if err != nil {
		return pagination.Result[TenantRole]{}, err
	}
	request, err := pagination.Normalize(input.Request)
	input.Keyword = strings.TrimSpace(input.Keyword)
	if err != nil || len(input.Keyword) > maxRoleKeywordLength || len(input.IDs) > 200 || len(input.Statuses) > 10 || !boundedAuthorizationIDs(input.IDs) || (input.CreatedAtFrom != nil && input.CreatedAtTo != nil && !input.CreatedAtFrom.Before(*input.CreatedAtTo)) {
		return pagination.Result[TenantRole]{}, ErrTenantAuthorizationInvalid
	}
	for _, status := range input.Statuses {
		if status != "active" && status != "disabled" {
			return pagination.Result[TenantRole]{}, ErrTenantAuthorizationInvalid
		}
	}
	scope, err := s.roleScope(ctx)
	if err != nil {
		return pagination.Result[TenantRole]{}, err
	}
	items, total, err := s.pageRoles(ctx, actor.TenantID, input, request, scope)
	if err != nil {
		return pagination.Result[TenantRole]{}, err
	}
	if err := s.presentRoles(ctx, items); err != nil {
		return pagination.Result[TenantRole]{}, err
	}
	return pagination.Result[TenantRole]{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func (s *TenantAuthorizationService) pageRoles(ctx context.Context, tenantID string, input RolePageInput, request pagination.Request, scope datapermission.SQLPredicate) ([]TenantRole, int64, error) {
	where, args := `tr.tenant_id=? AND tr.deleted_at IS NULL AND `+scope.Clause, append([]any{tenantID}, scope.Args...)
	if keyword := input.Keyword; keyword != "" {
		where += ` AND (LOWER(tr.code) LIKE ? OR LOWER(tr.name) LIKE ? OR LOWER(tr.description) LIKE ?)`
		pattern := "%" + strings.ToLower(keyword) + "%"
		args = append(args, pattern, pattern, pattern)
	}
	for _, filter := range []struct {
		column string
		values []string
	}{{"tr.id", input.IDs}, {"tr.status", input.Statuses}} {
		column, values := filter.column, filter.values
		if len(values) == 0 {
			continue
		}
		clause, inArgs, inErr := sqlx.In(column+` IN (?)`, values)
		if inErr != nil {
			return nil, 0, ErrTenantAuthorizationInvalid
		}
		where += " AND " + clause
		args = append(args, inArgs...)
	}
	if input.CreatedAtFrom != nil {
		where += ` AND tr.created_at>=?`
		args = append(args, *input.CreatedAtFrom)
	}
	if input.CreatedAtTo != nil {
		where += ` AND tr.created_at<?`
		args = append(args, *input.CreatedAtTo)
	}
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*) FROM tenant_roles tr WHERE `+where), args...); err != nil {
		return nil, 0, err
	}
	queryArgs := append(append([]any{}, args...), request.PageSize, pagination.Offset(request))
	items := []TenantRole{}
	query := `SELECT tr.id,tr.tenant_id,tr.code,tr.name,tr.description,tr.status,tr.created_at,tr.created_by,tr.updated_at,tr.updated_by,tr.version FROM tenant_roles tr WHERE ` + where + ` ORDER BY tr.created_at DESC,tr.id LIMIT ? OFFSET ?`
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(query), queryArgs...); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *TenantAuthorizationService) presentRoles(ctx context.Context, roles []TenantRole) error {
	ids := make([]string, 0, len(roles)*2)
	names := make(map[string]string, len(roles)*2)
	for _, role := range roles {
		for _, id := range []string{role.CreatedBy, role.UpdatedBy} {
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
	for index := range roles {
		roles[index].CreatedByName = names[roles[index].CreatedBy]
		roles[index].UpdatedByName = names[roles[index].UpdatedBy]
		roles[index].CreatedAt = presentation.Time(roles[index].CreatedAt)
		roles[index].UpdatedAt = presentation.Time(roles[index].UpdatedAt)
	}
	return nil
}

func (s *TenantAuthorizationService) RolePermissions(ctx context.Context, roleID string) ([]PermissionView, error) {
	actor, err := tenantActor(ctx)
	if err != nil {
		return nil, err
	}
	roleID = strings.TrimSpace(roleID)
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
	membershipID = strings.TrimSpace(membershipID)
	if membershipID == "" || len(membershipID) > maxAuthorizationIDLength {
		return nil, ErrTenantAuthorizationInvalid
	}
	scope, err := s.memberScope(ctx)
	if err != nil {
		return nil, err
	}
	var count int
	args := append([]any{actor.TenantID, membershipID}, scope.Args...)
	if err := s.db.GetContext(ctx, &count, s.db.Rebind(`SELECT count(*) FROM tenant_memberships tm WHERE tm.tenant_id=? AND tm.id=? AND tm.deleted_at IS NULL AND `+scope.Clause), args...); err != nil {
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
	roleID, name, description = strings.TrimSpace(roleID), strings.TrimSpace(name), strings.TrimSpace(description)
	if roleID == "" || len(roleID) > maxAuthorizationIDLength || name == "" || len(name) > maxRoleNameLength || len(description) > maxRoleDescriptionLength || version <= 0 || (status != "active" && status != "disabled") {
		return TenantRole{}, ErrTenantAuthorizationInvalid
	}
	scope, err := s.roleScope(ctx)
	if err != nil {
		return TenantRole{}, err
	}
	current, err := s.getRole(ctx, actor.TenantID, roleID, scope)
	if err != nil {
		return TenantRole{}, err
	}
	if s.dataScopes != nil {
		currentAttributes := datapermission.ResourceAttributes{"id": current.ID, "code": current.Code, "name": current.Name, "status": current.Status, "created_by": current.CreatedBy}
		proposedAttributes := datapermission.ResourceAttributes{"id": current.ID, "code": current.Code, "name": name, "status": status, "created_by": current.CreatedBy}
		if err := s.dataScopes.AuthorizeTransition(ctx, "tenant.role", currentAttributes, proposedAttributes); err != nil {
			if errors.Is(err, datapermission.ErrObjectDenied) {
				return TenantRole{}, ErrTenantAuthorizationForbidden
			}
			return TenantRole{}, err
		}
	}
	err = s.withLock(ctx, "tenant:"+actor.TenantID+":role:"+roleID, func(ctx context.Context) error {
		return s.mutate(ctx, "tenant.role.update", roleID, map[string]any{"name": name, "status": status, "version": version}, actor.TenantID, nil, func(tx *sqlx.Tx) error {
			if err := s.ensureRoleAssignable(ctx, tx, actor, roleID); err != nil {
				return err
			}
			args := append([]any{name, description, status, time.Now(), actor.ID, actor.TenantID, roleID, version}, scope.Args...)
			result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_roles AS tr SET name=?,description=?,status=?,updated_at=?,updated_by=?,version=version+1 WHERE tr.tenant_id=? AND tr.id=? AND tr.version=? AND tr.deleted_at IS NULL AND `+scope.Clause), args...)
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
	if err != nil {
		return TenantRole{}, err
	}
	return s.getRole(ctx, actor.TenantID, roleID, unrestrictedScope())
}

func (s *TenantAuthorizationService) DeleteRole(ctx context.Context, roleID string, version int64) error {
	actor, err := tenantActor(ctx)
	if err != nil {
		return err
	}
	roleID = strings.TrimSpace(roleID)
	if roleID == "" || len(roleID) > maxAuthorizationIDLength || version <= 0 {
		return ErrTenantAuthorizationInvalid
	}
	scope, err := s.roleScope(ctx)
	if err != nil {
		return err
	}
	current, err := s.getRole(ctx, actor.TenantID, roleID, scope)
	if err != nil {
		return err
	}
	roleName := current.Name
	return s.withLock(ctx, "tenant:"+actor.TenantID+":role:"+roleID, func(ctx context.Context) error {
		return s.mutate(ctx, "tenant.role.delete", roleID, map[string]any{"name": roleName, "version": version}, actor.TenantID, nil, func(tx *sqlx.Tx) error {
			if err := s.ensureRoleAssignable(ctx, tx, actor, roleID); err != nil {
				return err
			}
			now := time.Now()
			for _, table := range []string{"tenant_member_roles", "tenant_role_permissions"} {
				if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE `+table+` SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND role_id=? AND deleted_at IS NULL`), now, actor.ID, now, actor.ID, actor.TenantID, roleID); err != nil {
					return err
				}
			}
			args := append([]any{now, actor.ID, now, actor.ID, actor.TenantID, roleID, version}, scope.Args...)
			result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE tenant_roles AS tr SET deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE tr.tenant_id=? AND tr.id=? AND tr.version=? AND tr.deleted_at IS NULL AND `+scope.Clause), args...)
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
}

func (s *TenantAuthorizationService) roleScope(ctx context.Context) (datapermission.SQLPredicate, error) {
	if s.dataScopes == nil {
		actor, err := platformprincipal.Require(ctx)
		if err == nil && actor.Type == platformprincipal.TypeSystem {
			return unrestrictedScope(), nil
		}
		return datapermission.SQLPredicate{}, datapermission.ErrScopeRequired
	}
	return s.dataScopes.Compile(ctx, "tenant.role")
}

func (s *TenantAuthorizationService) memberScope(ctx context.Context) (datapermission.SQLPredicate, error) {
	if s.dataScopes == nil {
		actor, err := platformprincipal.Require(ctx)
		if err == nil && actor.Type == platformprincipal.TypeSystem {
			return unrestrictedScope(), nil
		}
		return datapermission.SQLPredicate{}, datapermission.ErrScopeRequired
	}
	return s.dataScopes.Compile(ctx, "tenant.member")
}

func (s *TenantAuthorizationService) scopedMembershipName(ctx context.Context, tenantID, membershipID string, scope datapermission.SQLPredicate) (string, error) {
	var snapshot struct {
		Username    string `db:"username"`
		DisplayName string `db:"display_name"`
	}
	args := append([]any{tenantID, membershipID}, scope.Args...)
	query := s.db.Rebind(`SELECT tm.username,tm.display_name FROM tenant_memberships tm WHERE tm.tenant_id=? AND tm.id=? AND tm.deleted_at IS NULL AND ` + scope.Clause)
	if err := s.db.GetContext(ctx, &snapshot, query, args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrTenantAuthorizationNotFound
		}
		return "", err
	}
	if name := strings.TrimSpace(snapshot.DisplayName); name != "" {
		return name, nil
	}
	return strings.TrimSpace(snapshot.Username), nil
}

func unrestrictedScope() datapermission.SQLPredicate {
	return datapermission.SQLPredicate{Clause: "(1 = 1)", Args: []any{}}
}

func tenantActor(ctx context.Context) (platformprincipal.Principal, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.TenantID == "" || actor.MembershipID == "" {
		return actor, ErrTenantAuthorizationForbidden
	}
	return actor, nil
}

func normalizeIDs(ids []string) ([]string, error) {
	if len(ids) > 1000 {
		return nil, ErrTenantAuthorizationInvalid
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > maxAuthorizationIDLength {
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

func boundedAuthorizationIDs(ids []string) bool {
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || len(id) > maxAuthorizationIDLength {
			return false
		}
	}
	return true
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

func (s *TenantAuthorizationService) ensureAssignable(ctx context.Context, tx *sqlx.Tx, actor platformprincipal.Principal, wanted []string) error {
	if err := s.validateTenantPermissions(ctx, tx, wanted); err != nil {
		return err
	}
	effective, err := effectivePermissionIDs(ctx, tx, actor.TenantID, actor.MembershipID)
	if err != nil {
		return err
	}
	return requireSubset(wanted, effective)
}

func (s *TenantAuthorizationService) ensureRoleAssignable(ctx context.Context, tx *sqlx.Tx, actor platformprincipal.Principal, roleID string) error {
	permissions := []string{}
	query := `SELECT permission_id FROM tenant_role_permissions WHERE tenant_id=? AND role_id=? AND deleted_at IS NULL`
	if err := tx.SelectContext(ctx, &permissions, tx.Rebind(query), actor.TenantID, roleID); err != nil {
		return err
	}
	var count int
	if err := tx.GetContext(ctx, &count, tx.Rebind(`SELECT count(*) FROM tenant_roles WHERE tenant_id=? AND id=? AND deleted_at IS NULL`), actor.TenantID, roleID); err != nil {
		return err
	}
	if count != 1 {
		return ErrTenantAuthorizationNotFound
	}
	return s.ensureAssignable(ctx, tx, actor, permissions)
}

func (s *TenantAuthorizationService) ensureRolesAssignable(ctx context.Context, tx *sqlx.Tx, actor platformprincipal.Principal, roleIDs []string) error {
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
	return s.ensureAssignable(ctx, tx, actor, permissions)
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

func (s *TenantAuthorizationService) validateTenantPermissions(ctx context.Context, tx *sqlx.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if s == nil || s.registry == nil {
		return ErrTenantAuthorizationInvalid
	}
	type registeredPermission struct {
		ID       string `db:"id"`
		Resource string `db:"resource"`
		Action   string `db:"action"`
	}
	query, args, inErr := sqlx.In(`SELECT id,resource,action FROM permissions WHERE id IN (?) AND node_type='permission' AND status='active' AND deleted_at IS NULL`, ids)
	if inErr != nil {
		return fmt.Errorf("build permission validation query: %w", inErr)
	}
	permissions := []registeredPermission{}
	if err := tx.SelectContext(ctx, &permissions, tx.Rebind(query), args...); err != nil {
		return err
	}
	if len(permissions) != len(ids) {
		return ErrTenantAuthorizationInvalid
	}
	for _, permission := range permissions {
		definition, _, err := s.registry.Resolve(permission.Resource, permission.Action)
		if err != nil || definition.Scope != pbac.ResourceScopeTenant {
			return ErrTenantAuthorizationInvalid
		}
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

func lockTenant(ctx context.Context, tx *sqlx.Tx, tenantID string) error {
	var id string
	if err := tx.GetContext(ctx, &id, tx.Rebind(`SELECT id FROM tenants WHERE id=? AND status='active' AND deleted_at IS NULL FOR UPDATE`), tenantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTenantAuthorizationNotFound
		}
		return err
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

func ensureAdministrator(ctx context.Context, tx *sqlx.Tx, tenantID, membershipID string) error {
	var count int
	query := `SELECT count(*) FROM tenant_administrators a JOIN tenant_memberships m ON m.tenant_id=a.tenant_id AND m.id=a.membership_id AND m.status='active' AND m.deleted_at IS NULL JOIN tenants t ON t.id=a.tenant_id AND t.status='active' AND t.deleted_at IS NULL WHERE a.tenant_id=? AND a.membership_id=? AND a.deleted_at IS NULL`
	if err := tx.GetContext(ctx, &count, tx.Rebind(query), tenantID, membershipID); err != nil {
		return err
	}
	if count != 1 {
		return ErrTenantAuthorizationForbidden
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
		_, err = tx.ExecContext(ctx, tx.Rebind(`UPDATE `+table+` SET deleted_at=NULL,deleted_by=NULL,updated_at=?,updated_by=?,version=version+1 WHERE tenant_id=? AND id=?`), now, actorID, tenantID, id)
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
	if database.IsUniqueViolation(err) {
		return ErrTenantAuthorizationConflict
	}
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
	err := cache.WithLock(ctx, s.locker, key, s.cfg.DistributedLock.TTL, s.cfg.DistributedLock.RetryDelay, func(lockCtx context.Context) error {
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

func (s *TenantAuthorizationService) mutate(ctx context.Context, operation, resourceID string, request any, tenantID string, options *sql.TxOptions, fn func(*sqlx.Tx) error) error {
	started := time.Now()
	resourceName := authorizationMutationName(request, resourceID)
	operationEntry := operationlog.Entry{Operation: operation, ResourceType: "tenant_authorization", ResourceID: resourceID, ResourceName: resourceName, Source: "backend", Protocol: "service", Request: request}
	securityEntry := securitylog.Entry{EventType: securitylog.EventTenantAuthorization, SubjectID: resourceID, SubjectName: resourceName, SubjectType: "tenant_authorization", TenantID: tenantID, Metadata: map[string]any{"operation": operation}}
	err := s.transactor.Within(ctx, options, func(tx *sqlx.Tx) error {
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
	}
	return err
}

func authorizationMutationName(request any, fallback string) string {
	if value, ok := request.(map[string]any); ok {
		if name, ok := value["name"].(string); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	return fallback
}

func (s *TenantAuthorizationService) tenantName(ctx context.Context, tenantID string) (string, error) {
	var name string
	if err := s.db.GetContext(ctx, &name, s.db.Rebind(`SELECT name FROM tenants WHERE id=? AND deleted_at IS NULL`), tenantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrTenantAuthorizationNotFound
		}
		return "", err
	}
	return strings.TrimSpace(name), nil
}

func (s *TenantAuthorizationService) membershipName(ctx context.Context, tenantID, membershipID string) (string, error) {
	var member struct {
		Username    string `db:"username"`
		DisplayName string `db:"display_name"`
	}
	query := s.db.Rebind(`SELECT username,display_name FROM tenant_memberships WHERE tenant_id=? AND id=? AND deleted_at IS NULL`)
	if err := s.db.GetContext(ctx, &member, query, tenantID, membershipID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrTenantAuthorizationNotFound
		}
		return "", err
	}
	if name := strings.TrimSpace(member.DisplayName); name != "" {
		return name, nil
	}
	return strings.TrimSpace(member.Username), nil
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
