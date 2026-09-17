package datapermission

import (
	"context"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var ErrScopeRequired = errors.New("data permission: endpoint scope is required")

// Service resolves trusted subject projections and compiles the scope declared
// by the already-authorized endpoint.
type Service struct {
	db     *sqlx.DB
	engine *Engine
}

func NewService(db *sqlx.DB, engine *Engine) *Service { return &Service{db: db, engine: engine} }

func (s *Service) Compile(ctx context.Context, expectedResource string) (SQLPredicate, error) {
	principal, err := platformprincipal.Require(ctx)
	if err != nil {
		return SQLPredicate{}, err
	}
	endpoint, ok := accesscontrol.EndpointFromContext(ctx)
	if !ok || endpoint.Resource != expectedResource || endpoint.DataPermission == accesscontrol.DataPermissionNone {
		if principal.Type == platformprincipal.TypeSystem {
			return SQLPredicate{Clause: "(1 = 1)", Args: []any{}}, nil
		}
		return SQLPredicate{}, ErrScopeRequired
	}
	roles, departments, err := s.projections(ctx, principal)
	if err != nil {
		return SQLPredicate{}, fmt.Errorf("resolve data permission subject: %w", err)
	}
	subject := pbac.Subject{ID: principal.ID, Type: string(principal.Type), Authenticated: true, TenantID: principal.TenantID, MembershipID: principal.MembershipID, Roles: roles}
	attributes := SubjectAttributes{"id": principal.ID, "tenant_id": principal.TenantID, "membership_id": principal.MembershipID, "role_codes": roles, "department_ids": departments}
	return s.engine.CompileSQL(ctx, endpoint.Resource, endpoint.Action, subject, attributes)
}

// AuthorizeObject evaluates a trusted proposed object for an endpoint that
// explicitly declares DataPermissionObject. No matching Allow fails closed.
func (s *Service) AuthorizeObject(ctx context.Context, expectedResource string, resource ResourceAttributes) error {
	return s.authorizeObject(ctx, expectedResource, accesscontrol.DataPermissionObject, resource, resource)
}

// AuthorizeTransition checks a trusted current object and a separately
// constructed target object. It is used after the current row has been selected
// under Compile's SQL predicate and before the mutation is committed.
func (s *Service) AuthorizeTransition(ctx context.Context, expectedResource string, resource, proposed ResourceAttributes) error {
	return s.authorizeObject(ctx, expectedResource, accesscontrol.DataPermissionRequired, resource, proposed)
}

func (s *Service) authorizeObject(ctx context.Context, expectedResource string, expectedMode accesscontrol.DataPermissionMode, resource, proposed ResourceAttributes) error {
	principal, err := platformprincipal.Require(ctx)
	if err != nil {
		return err
	}
	endpoint, ok := accesscontrol.EndpointFromContext(ctx)
	if !ok || endpoint.Resource != expectedResource || endpoint.DataPermission != expectedMode {
		if principal.Type == platformprincipal.TypeSystem {
			return nil
		}
		return ErrScopeRequired
	}
	roles, departments, err := s.projections(ctx, principal)
	if err != nil {
		return fmt.Errorf("resolve data permission subject: %w", err)
	}
	subject := pbac.Subject{ID: principal.ID, Type: string(principal.Type), Authenticated: true, TenantID: principal.TenantID, MembershipID: principal.MembershipID, Roles: roles}
	attributes := SubjectAttributes{"id": principal.ID, "tenant_id": principal.TenantID, "membership_id": principal.MembershipID, "role_codes": roles, "department_ids": departments}
	allowed, err := s.engine.EvaluateTransition(ctx, endpoint.Resource, endpoint.Action, subject, attributes, resource, proposed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrObjectDenied
	}
	return nil
}

func (s *Service) projections(ctx context.Context, principal platformprincipal.Principal) ([]string, []string, error) {
	if principal.Type != platformprincipal.TypeUser || principal.TenantID == "" || principal.MembershipID == "" {
		return nil, nil, nil
	}
	roles, departments := []string{}, []string{}
	roleQuery := s.db.Rebind(`SELECT DISTINCT r.code FROM tenant_member_roles mr JOIN tenant_memberships m ON m.id=mr.membership_id AND m.tenant_id=mr.tenant_id AND m.user_id=? AND m.status='active' AND m.deleted_at IS NULL JOIN tenant_roles r ON r.id=mr.role_id AND r.tenant_id=mr.tenant_id WHERE mr.tenant_id=? AND mr.membership_id=? AND mr.deleted_at IS NULL AND r.status='active' AND r.deleted_at IS NULL ORDER BY r.code`)
	if err := s.db.SelectContext(ctx, &roles, roleQuery, principal.ID, principal.TenantID, principal.MembershipID); err != nil {
		return nil, nil, err
	}
	departmentQuery := s.db.Rebind(`SELECT DISTINCT dm.department_id FROM tenant_department_members dm JOIN tenant_memberships m ON m.id=dm.membership_id AND m.tenant_id=dm.tenant_id AND m.user_id=? AND m.status='active' AND m.deleted_at IS NULL WHERE dm.tenant_id=? AND dm.membership_id=? AND dm.deleted_at IS NULL ORDER BY dm.department_id`)
	if err := s.db.SelectContext(ctx, &departments, departmentQuery, principal.ID, principal.TenantID, principal.MembershipID); err != nil {
		return nil, nil, err
	}
	return roles, departments, nil
}
