package authorization

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

// Authorizer adapts the in-process PBAC engine to the shared enforcement
// contract. Tenant role codes are resolved from trusted local tables.
type Authorizer struct {
	db       *sqlx.DB
	engine   *pbac.Engine
	registry *pbac.Registry
}

func New(db *sqlx.DB, engine *pbac.Engine, registry *pbac.Registry) platformauthz.Authorizer {
	return &Authorizer{db: db, engine: engine, registry: registry}
}

func (a *Authorizer) Authorize(ctx context.Context, principal platformprincipal.Principal, requirement platformauthz.Requirement) error {
	if a == nil || a.db == nil || a.engine == nil || a.registry == nil {
		return platformauthz.ErrDecisionUnavailable
	}
	resource, _, err := a.registry.Resolve(requirement.Resource, requirement.Action)
	if err != nil || !scopeMatches(resource.Scope, requirement.Scope) {
		return platformauthz.ErrDecisionUnavailable
	}
	roles, err := a.roles(ctx, principal)
	if err != nil {
		return platformauthz.ErrDecisionUnavailable
	}
	decision, err := a.engine.Evaluate(ctx, pbac.EvaluationRequest{
		Subject: pbac.Subject{
			ID: principal.ID, Type: string(principal.Type), Authenticated: true,
			TenantID: principal.TenantID, MembershipID: principal.MembershipID, Roles: roles,
		},
		Resource: pbac.Resource{
			Type: requirement.Resource, TenantID: principal.TenantID,
		},
		Action: requirement.Action,
	})
	if err != nil || decision.Effect == pbac.DecisionEffectIndeterminate {
		return platformauthz.ErrDecisionUnavailable
	}
	if decision.Effect == pbac.DecisionEffectAllow {
		return nil
	}
	// Published PBAC deny policies are authoritative. Tenant role grants are a
	// managed allow source and may only fill the no-matching-policy case.
	if decision.ReasonCode != pbac.ReasonNoMatchingPolicy || resource.Scope != pbac.ResourceScopeTenant {
		return platformauthz.ErrDenied
	}
	allowed, err := a.hasManagedTenantGrant(ctx, principal, requirement)
	if err != nil {
		return platformauthz.ErrDecisionUnavailable
	}
	if !allowed {
		return platformauthz.ErrDenied
	}
	return nil
}

func scopeMatches(resourceScope pbac.ResourceScope, requirementScope platformauthz.Scope) bool {
	return resourceScope == pbac.ResourceScopeTenant && requirementScope == platformauthz.ScopeTenant ||
		resourceScope == pbac.ResourceScopePlatform && requirementScope == platformauthz.ScopePlatform ||
		resourceScope == pbac.ResourceScopePrincipal && requirementScope == platformauthz.ScopePrincipal
}

func (a *Authorizer) hasManagedTenantGrant(ctx context.Context, principal platformprincipal.Principal, requirement platformauthz.Requirement) (bool, error) {
	if principal.Type != platformprincipal.TypeUser || principal.TenantID == "" || principal.MembershipID == "" {
		return false, nil
	}
	query := a.db.Rebind(`SELECT count(*)
		FROM permissions p
		JOIN tenant_permission_grants g ON g.permission_id=p.id AND g.tenant_id=? AND g.deleted_at IS NULL
		JOIN tenant_memberships m ON m.tenant_id=g.tenant_id AND m.id=? AND m.user_id=? AND m.status='active' AND m.deleted_at IS NULL
		JOIN tenants t ON t.id=g.tenant_id AND t.status='active' AND t.deleted_at IS NULL
		WHERE p.resource=? AND p.action=? AND p.node_type='permission' AND p.status='active' AND p.deleted_at IS NULL
		  AND (
			EXISTS (SELECT 1 FROM tenant_administrators a WHERE a.tenant_id=g.tenant_id AND a.membership_id=m.id AND a.deleted_at IS NULL)
			OR EXISTS (
				SELECT 1 FROM tenant_member_roles mr
				JOIN tenant_roles r ON r.id=mr.role_id AND r.tenant_id=mr.tenant_id AND r.status='active' AND r.deleted_at IS NULL
				JOIN tenant_role_permissions rp ON rp.tenant_id=mr.tenant_id AND rp.role_id=mr.role_id AND rp.permission_id=p.id AND rp.deleted_at IS NULL
				WHERE mr.tenant_id=g.tenant_id AND mr.membership_id=m.id AND mr.deleted_at IS NULL
			)
		)`)
	var count int
	if err := a.db.GetContext(ctx, &count, query, principal.TenantID, principal.MembershipID, principal.ID, requirement.Resource, requirement.Action); err != nil {
		return false, fmt.Errorf("resolve managed tenant grant: %w", err)
	}
	return count > 0, nil
}

func (a *Authorizer) roles(ctx context.Context, principal platformprincipal.Principal) ([]string, error) {
	if principal.Type != platformprincipal.TypeUser || principal.TenantID == "" || principal.MembershipID == "" {
		return nil, nil
	}
	roles := []string{}
	query := a.db.Rebind(`SELECT DISTINCT r.code
		FROM tenant_member_roles mr
		JOIN tenant_memberships m ON m.id=mr.membership_id AND m.tenant_id=mr.tenant_id AND m.user_id=? AND m.status='active' AND m.deleted_at IS NULL
		JOIN tenant_roles r ON r.id=mr.role_id AND r.tenant_id=mr.tenant_id
		WHERE mr.tenant_id=? AND mr.membership_id=?
		  AND mr.deleted_at IS NULL AND r.status='active' AND r.deleted_at IS NULL
		ORDER BY r.code`)
	if err := a.db.SelectContext(ctx, &roles, query, principal.ID, principal.TenantID, principal.MembershipID); err != nil {
		return nil, fmt.Errorf("resolve pbac subject roles: %w", err)
	}
	return roles, nil
}
