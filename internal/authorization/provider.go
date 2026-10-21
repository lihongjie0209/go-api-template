package authorization

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

// Authorizer resolves operation grants; data permissions remain an independent boundary.
type Authorizer struct {
	db       *sqlx.DB
	registry *resourceaction.Registry
}

type authorizationMemoKey struct{}
type authorizationMemo struct {
	grantsLoaded bool
	grants       map[string]struct{}
	grantsErr    error
}

func withAuthorizationMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, authorizationMemoKey{}, &authorizationMemo{})
}

func New(db *sqlx.DB, registry *resourceaction.Registry) platformauthz.Authorizer {
	return &Authorizer{db: db, registry: registry}
}

func (a *Authorizer) Authorize(ctx context.Context, principal platformprincipal.Principal, requirement platformauthz.Requirement) error {
	if a == nil || a.db == nil || a.registry == nil {
		return platformauthz.ErrDecisionUnavailable
	}
	resource, _, err := a.registry.Resolve(requirement.Resource, requirement.Action)
	if err != nil || !scopeMatches(resource.Scope, requirement.Scope) {
		return platformauthz.ErrDecisionUnavailable
	}
	if principal.ID == "" {
		return platformauthz.ErrDenied
	}
	if principal.Type == platformprincipal.TypeSystem {
		return nil
	}
	switch resource.Scope {
	case resourceaction.ResourceScopePrincipal:
		return nil // The owning service checks current-principal object ownership.
	case resourceaction.ResourceScopePlatform:
		allowed, err := a.hasPlatformGrant(ctx, principal, requirement)
		if err != nil {
			return platformauthz.ErrDecisionUnavailable
		}
		if allowed {
			return nil
		}
	case resourceaction.ResourceScopeTenant:
		allowed, err := a.hasManagedTenantGrant(ctx, principal, requirement)
		if err != nil {
			return platformauthz.ErrDecisionUnavailable
		}
		if allowed {
			return nil
		}
	}
	return platformauthz.ErrDenied
}

func scopeMatches(resourceScope resourceaction.ResourceScope, requirementScope platformauthz.Scope) bool {
	return resourceScope == resourceaction.ResourceScopeTenant && requirementScope == platformauthz.ScopeTenant ||
		resourceScope == resourceaction.ResourceScopePlatform && requirementScope == platformauthz.ScopePlatform ||
		resourceScope == resourceaction.ResourceScopePrincipal && requirementScope == platformauthz.ScopePrincipal
}

func (a *Authorizer) hasPlatformGrant(ctx context.Context, principal platformprincipal.Principal, requirement platformauthz.Requirement) (bool, error) {
	query := a.db.Rebind(`SELECT count(*) FROM platform_principal_permissions g
		JOIN permissions p ON p.id=g.permission_id AND p.node_type='permission' AND p.status='active' AND p.deleted_at IS NULL
		JOIN permission_resource_actions link ON link.permission_id=p.id AND link.deleted_at IS NULL
		JOIN authorization_resource_actions act ON act.id=link.action_id AND act.status='active' AND act.deleted_at IS NULL
		JOIN authorization_resources res ON res.id=act.resource_id AND res.scope='platform' AND res.status='active' AND res.deleted_at IS NULL
		WHERE g.principal_type=? AND g.principal_id=? AND g.deleted_at IS NULL AND res.resource_key=? AND act.action_key=?`)
	var count int
	if err := a.db.GetContext(ctx, &count, query, string(principal.Type), principal.ID, requirement.Resource, requirement.Action); err != nil {
		return false, fmt.Errorf("resolve platform grant: %w", err)
	}
	return count > 0, nil
}

func (a *Authorizer) hasManagedTenantGrant(ctx context.Context, principal platformprincipal.Principal, requirement platformauthz.Requirement) (bool, error) {
	if principal.Type != platformprincipal.TypeUser || principal.TenantID == "" || principal.MembershipID == "" {
		return false, nil
	}
	if memo, ok := ctx.Value(authorizationMemoKey{}).(*authorizationMemo); ok {
		if !memo.grantsLoaded {
			memo.grants, memo.grantsErr = a.managedTenantGrants(ctx, principal)
			memo.grantsLoaded = true
		}
		if memo.grantsErr != nil {
			return false, memo.grantsErr
		}
		_, allowed := memo.grants[requirement.Resource+"\x00"+requirement.Action]
		return allowed, nil
	}
	grants, err := a.managedTenantGrants(ctx, principal)
	if err != nil {
		return false, err
	}
	_, allowed := grants[requirement.Resource+"\x00"+requirement.Action]
	return allowed, nil
}

func (a *Authorizer) managedTenantGrants(ctx context.Context, principal platformprincipal.Principal) (map[string]struct{}, error) {
	query := a.db.Rebind(`SELECT DISTINCT res.resource_key,act.action_key FROM permissions p
		JOIN permission_resource_actions link ON link.permission_id=p.id AND link.deleted_at IS NULL
		JOIN authorization_resource_actions act ON act.id=link.action_id AND act.status='active' AND act.deleted_at IS NULL
		JOIN authorization_resources res ON res.id=act.resource_id AND res.scope='tenant' AND res.status='active' AND res.deleted_at IS NULL
		JOIN tenant_permission_grants g ON g.permission_id=p.id AND g.tenant_id=? AND g.deleted_at IS NULL
		JOIN tenant_memberships m ON m.tenant_id=g.tenant_id AND m.id=? AND m.user_id=? AND m.status='active' AND m.deleted_at IS NULL
		JOIN tenants t ON t.id=g.tenant_id AND t.status='active' AND t.deleted_at IS NULL
		WHERE p.node_type='permission' AND p.status='active' AND p.deleted_at IS NULL
		AND (EXISTS (SELECT 1 FROM tenant_administrators a WHERE a.tenant_id=g.tenant_id AND a.membership_id=m.id AND a.deleted_at IS NULL)
		OR EXISTS (SELECT 1 FROM tenant_member_roles mr JOIN tenant_roles r ON r.id=mr.role_id AND r.tenant_id=mr.tenant_id AND r.status='active' AND r.deleted_at IS NULL
		JOIN tenant_role_permissions rp ON rp.tenant_id=mr.tenant_id AND rp.role_id=mr.role_id AND rp.permission_id=p.id AND rp.deleted_at IS NULL
		WHERE mr.tenant_id=g.tenant_id AND mr.membership_id=m.id AND mr.deleted_at IS NULL))`)
	rows := []struct {
		Resource string `db:"resource_key"`
		Action   string `db:"action_key"`
	}{}
	if err := a.db.SelectContext(ctx, &rows, query, principal.TenantID, principal.MembershipID, principal.ID); err != nil {
		return nil, fmt.Errorf("resolve managed tenant grants: %w", err)
	}
	result := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		result[row.Resource+"\x00"+row.Action] = struct{}{}
	}
	return result, nil
}
