package authorization

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
	"github.com/lihongjie0209/go-api-template/internal/environment"
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
	now      func() time.Time
}

type authorizationMemoKey struct{}

type authorizationMemo struct {
	rolesLoaded  bool
	roles        []string
	rolesErr     error
	grantsLoaded bool
	grants       map[string]struct{}
	grantsErr    error
}

func withAuthorizationMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, authorizationMemoKey{}, &authorizationMemo{})
}

var operationLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

func New(db *sqlx.DB, engine *pbac.Engine, registry *pbac.Registry) platformauthz.Authorizer {
	return newAuthorizer(db, engine, registry, time.Now)
}

func newAuthorizer(db *sqlx.DB, engine *pbac.Engine, registry *pbac.Registry, now func() time.Time) *Authorizer {
	return &Authorizer{db: db, engine: engine, registry: registry, now: now}
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
	now := a.now
	if now == nil {
		now = time.Now
	}
	decisionTime := now().In(operationLocation)
	endpoint, _ := accesscontrol.EndpointFromContext(ctx)
	profile, _ := environment.FromContext(ctx)
	credential, _ := accesscontrol.CredentialSchemeFromContext(ctx)
	weekday := int(decisionTime.Weekday())
	if weekday == 0 {
		weekday = 7
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
		Context: pbac.OperationContext{
			Transport: string(endpoint.Transport), Operation: endpoint.Operation,
			Profile: profile, Timezone: "Asia/Shanghai", LocalHour: decisionTime.Hour(), Weekday: weekday,
			BusinessDay: weekday <= 5, AuthenticationScheme: string(credential),
		},
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

func (a *Authorizer) managedTenantGrants(ctx context.Context, principal platformprincipal.Principal) (map[string]struct{}, error) {
	query := a.db.Rebind(`SELECT DISTINCT p.resource,p.action
		FROM permissions p
		JOIN tenant_permission_grants g ON g.permission_id=p.id AND g.tenant_id=? AND g.deleted_at IS NULL
		JOIN tenant_memberships m ON m.tenant_id=g.tenant_id AND m.id=? AND m.user_id=? AND m.status='active' AND m.deleted_at IS NULL
		JOIN tenants t ON t.id=g.tenant_id AND t.status='active' AND t.deleted_at IS NULL
		WHERE p.node_type='permission' AND p.status='active' AND p.deleted_at IS NULL
		  AND (
			EXISTS (SELECT 1 FROM tenant_administrators a WHERE a.tenant_id=g.tenant_id AND a.membership_id=m.id AND a.deleted_at IS NULL)
			OR EXISTS (
				SELECT 1 FROM tenant_member_roles mr
				JOIN tenant_roles r ON r.id=mr.role_id AND r.tenant_id=mr.tenant_id AND r.status='active' AND r.deleted_at IS NULL
				JOIN tenant_role_permissions rp ON rp.tenant_id=mr.tenant_id AND rp.role_id=mr.role_id AND rp.permission_id=p.id AND rp.deleted_at IS NULL
				WHERE mr.tenant_id=g.tenant_id AND mr.membership_id=m.id AND mr.deleted_at IS NULL
			)
		)`)
	rows := []struct {
		Resource string `db:"resource"`
		Action   string `db:"action"`
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

func (a *Authorizer) roles(ctx context.Context, principal platformprincipal.Principal) ([]string, error) {
	if principal.Type != platformprincipal.TypeUser || principal.TenantID == "" || principal.MembershipID == "" {
		return nil, nil
	}
	if memo, ok := ctx.Value(authorizationMemoKey{}).(*authorizationMemo); ok {
		if !memo.rolesLoaded {
			memo.roles, memo.rolesErr = a.queryRoles(ctx, principal)
			memo.rolesLoaded = true
		}
		return memo.roles, memo.rolesErr
	}
	return a.queryRoles(ctx, principal)
}

func (a *Authorizer) queryRoles(ctx context.Context, principal platformprincipal.Principal) ([]string, error) {
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
