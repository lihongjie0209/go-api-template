package authorization

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type grantSnapshotKey struct{}

const maxEffectiveGrantActions = 10_000

type grantSnapshot struct {
	principal platformprincipal.Principal
	platform  map[string]struct{}
	tenant    map[string]struct{}
}

// Snapshot reads the current principal's operation grants from the authoritative
// database and pins that immutable decision set to one capability request.
// The digest changes when an effective platform or tenant action changes.
func (a *Authorizer) Snapshot(ctx context.Context, principal platformprincipal.Principal) (context.Context, string, error) {
	fail := func(err error) (context.Context, string, error) {
		return invalidateGrantSnapshot(ctx), "", err
	}
	if ctx == nil || ctx.Err() != nil || a == nil || a.db == nil || a.registry == nil || principal.ID == "" {
		return fail(platformauthz.ErrDecisionUnavailable)
	}
	snapshot := grantSnapshot{principal: principal}
	switch principal.Type {
	case platformprincipal.TypeSystem:
		// System principals bypass operation grants, but remain a distinct revision scope.
	case platformprincipal.TypeUser, platformprincipal.TypeServiceAccount:
		// The platform and tenant grant sets must come from one database view.
		tx, err := a.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
		if err != nil {
			return fail(fmt.Errorf("%w: %v", platformauthz.ErrDecisionUnavailable, err))
		}
		defer func() { _ = tx.Rollback() }()
		snapshot.platform, err = a.platformGrants(ctx, tx, principal)
		if err != nil {
			return fail(fmt.Errorf("%w: %v", platformauthz.ErrDecisionUnavailable, err))
		}
		if principal.Type == platformprincipal.TypeUser && principal.TenantID != "" && principal.MembershipID != "" {
			snapshot.tenant, err = a.managedTenantGrants(ctx, tx, principal)
			if err != nil {
				return fail(fmt.Errorf("%w: %v", platformauthz.ErrDecisionUnavailable, err))
			}
		}
		if err := tx.Commit(); err != nil {
			return fail(fmt.Errorf("%w: %v", platformauthz.ErrDecisionUnavailable, err))
		}
	default:
		return fail(platformauthz.ErrDecisionUnavailable)
	}
	if ctx.Err() != nil {
		return fail(platformauthz.ErrDecisionUnavailable)
	}
	return context.WithValue(ctx, grantSnapshotKey{}, snapshot), snapshot.revision(), nil
}

func invalidateGrantSnapshot(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, grantSnapshotKey{}, grantSnapshot{})
}

func (a *Authorizer) platformGrants(ctx context.Context, reader grantReader, principal platformprincipal.Principal) (map[string]struct{}, error) {
	query := reader.Rebind(fmt.Sprintf(`SELECT DISTINCT res.resource_key,act.action_key FROM platform_principal_permissions g
		JOIN permissions p ON p.id=g.permission_id AND p.node_type='permission' AND p.status='active' AND p.deleted_at IS NULL
		JOIN permission_resource_actions link ON link.permission_id=p.id AND link.deleted_at IS NULL
		JOIN authorization_resource_actions act ON act.id=link.action_id AND act.status='active' AND act.deleted_at IS NULL
		JOIN authorization_resources res ON res.id=act.resource_id AND res.scope='platform' AND res.status='active' AND res.deleted_at IS NULL
		WHERE g.principal_type=? AND g.principal_id=? AND g.deleted_at IS NULL LIMIT %d`, maxEffectiveGrantActions+1))
	rows := []struct {
		Resource string `db:"resource_key"`
		Action   string `db:"action_key"`
	}{}
	if err := reader.SelectContext(ctx, &rows, query, string(principal.Type), principal.ID); err != nil {
		return nil, fmt.Errorf("resolve platform grants: %w", err)
	}
	if len(rows) > maxEffectiveGrantActions {
		return nil, errors.New("platform grant set exceeds maximum")
	}
	grants := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		grants[row.Resource+"\x00"+row.Action] = struct{}{}
	}
	return grants, nil
}

func (s grantSnapshot) matches(principal platformprincipal.Principal) bool {
	return s.principal.Type == principal.Type && s.principal.ID == principal.ID &&
		s.principal.TenantID == principal.TenantID && s.principal.MembershipID == principal.MembershipID &&
		s.principal.SessionID == principal.SessionID
}

func (s grantSnapshot) allowed(scope resourceaction.ResourceScope, resource, action string) bool {
	key := resource + "\x00" + action
	if scope == resourceaction.ResourceScopePlatform {
		_, ok := s.platform[key]
		return ok
	}
	_, ok := s.tenant[key]
	return ok
}

func (s grantSnapshot) revision() string {
	parts := []string{"operation-grants/v2", string(s.principal.Type), s.principal.ID, s.principal.TenantID, s.principal.MembershipID, s.principal.SessionID}
	for key := range s.platform {
		parts = append(parts, "platform:"+key)
	}
	for key := range s.tenant {
		parts = append(parts, "tenant:"+key)
	}
	slices.Sort(parts[6:])
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}
