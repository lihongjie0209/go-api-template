package routepolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type Repository struct{ db *sqlx.DB }

func NewRepository(db *sqlx.DB) *Repository { return &Repository{db: db} }

type policyRow struct {
	ID         string `db:"id"`
	RouteID    string `db:"route_id"`
	Expression string `db:"expression"`
	Version    int64  `db:"version"`
}

type permissionRow struct {
	PolicyID string `db:"policy_id"`
	ID       string `db:"id"`
	Key      string `db:"permission_key"`
	Resource string `db:"resource"`
	Action   string `db:"action"`
	Scope    string `db:"scope"`
}

func (r *Repository) Load(ctx context.Context) ([]Definition, error) {
	if r.db == nil {
		return nil, errors.New("route policy database is disabled")
	}
	policies := []policyRow{}
	if err := r.db.SelectContext(ctx, &policies, `SELECT id,route_id,expression,version FROM route_policy_definitions WHERE status='active' AND deleted_at IS NULL ORDER BY route_id`); err != nil {
		return nil, fmt.Errorf("select route policies: %w", err)
	}
	refs := []permissionRow{}
	query := `SELECT r.policy_id,p.id,p.permission_key,p.resource,p.action,r.scope
		FROM route_policy_permission_refs r
		JOIN permissions p ON p.id=r.permission_id AND p.deleted_at IS NULL AND p.status='active' AND p.node_type='permission'
		JOIN route_policy_definitions d ON d.id=r.policy_id AND d.deleted_at IS NULL AND d.status='active'
		WHERE r.deleted_at IS NULL ORDER BY r.policy_id,p.permission_key`
	if err := r.db.SelectContext(ctx, &refs, query); err != nil {
		return nil, fmt.Errorf("select route policy permissions: %w", err)
	}
	definitions := make([]Definition, 0, len(policies))
	byPolicy := make(map[string]*Definition, len(policies))
	for _, row := range policies {
		definition := Definition{ID: row.ID, RouteID: row.RouteID, Expression: row.Expression, Permissions: map[string]Permission{}, Version: row.Version}
		definitions = append(definitions, definition)
		byPolicy[row.ID] = &definitions[len(definitions)-1]
	}
	for _, row := range refs {
		definition, ok := byPolicy[row.PolicyID]
		if !ok {
			continue
		}
		scope, err := parseScope(row.Scope)
		if err != nil {
			return nil, fmt.Errorf("policy %q permission %q: %w", row.PolicyID, row.Key, err)
		}
		definition.Permissions[row.Key] = Permission{ID: row.ID, Key: row.Key, Resource: row.Resource, Action: row.Action, Scope: scope}
	}
	return definitions, nil
}

func parseScope(value string) (platformauthz.Scope, error) {
	switch value {
	case "tenant":
		return platformauthz.ScopeTenant, nil
	case "platform":
		return platformauthz.ScopePlatform, nil
	case "principal":
		return platformauthz.ScopePrincipal, nil
	default:
		return 0, fmt.Errorf("%w: unknown scope %q", ErrInvalid, value)
	}
}

func (r *Repository) Revision(ctx context.Context) (time.Time, error) {
	if r.db == nil {
		return time.Time{}, errors.New("route policy database is disabled")
	}
	var revision sql.NullTime
	query := `SELECT max(updated_at) FROM (
		SELECT updated_at FROM route_policy_definitions
		UNION ALL
		SELECT updated_at FROM route_policy_permission_refs
		UNION ALL
		SELECT p.updated_at FROM permissions p
		JOIN route_policy_permission_refs r ON r.permission_id=p.id AND r.deleted_at IS NULL
	) route_policy_revisions`
	if err := r.db.GetContext(ctx, &revision, query); err != nil {
		return time.Time{}, fmt.Errorf("select route policy revision: %w", err)
	}
	return revision.Time, nil
}

func (r *Repository) ActiveRouteIDs(ctx context.Context, serviceName string) ([]string, error) {
	ids := []string{}
	query := r.db.Rebind(`SELECT id FROM route_definitions WHERE service_name=? AND status='active' AND deleted_at IS NULL ORDER BY id`)
	if err := r.db.SelectContext(ctx, &ids, query, serviceName); err != nil {
		return nil, fmt.Errorf("select active routes: %w", err)
	}
	return ids, nil
}

func (r *Repository) SyncRoutes(ctx context.Context, routes []Route, actor string) error {
	if r.db == nil {
		return errors.New("route policy database is disabled")
	}
	if len(routes) == 0 {
		return errors.New("route sync requires at least one route")
	}
	if actor == "" {
		return errors.New("route sync requires an audit actor")
	}
	for _, route := range routes {
		if route.ServiceName != routes[0].ServiceName || route.Protocol != routes[0].Protocol {
			return errors.New("route sync batch must share service name and protocol")
		}
	}
	actorCtx := platformprincipal.SystemContext(ctx, actor)
	return database.NewTransactor(r.db).Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		if _, err := tx.ExecContext(actorCtx, tx.Rebind(`UPDATE route_definitions SET status='inactive',updated_at=?,updated_by=? WHERE service_name=? AND protocol=? AND status='active' AND deleted_at IS NULL`), now, actor, routes[0].ServiceName, routes[0].Protocol); err != nil {
			return fmt.Errorf("deactivate undiscovered routes: %w", err)
		}
		for _, route := range routes {
			if err := upsertRoute(actorCtx, tx, route, actor, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func upsertRoute(ctx context.Context, tx *sqlx.Tx, route Route, actor string, now time.Time) error {
	var exists int
	err := tx.GetContext(ctx, &exists, tx.Rebind(`SELECT 1 FROM route_definitions WHERE id=? AND deleted_at IS NULL`), route.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		query := `INSERT INTO route_definitions(id,protocol,method,path,operation,description,service_name,source_version,status,last_discovered_at,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,1)`
		_, err = tx.ExecContext(ctx, tx.Rebind(query), route.ID, route.Protocol, route.Method, route.Path, route.Operation, route.Description, route.ServiceName, route.SourceVersion, "active", now, now, actor, now, actor)
	case err == nil:
		query := `UPDATE route_definitions SET protocol=?,method=?,path=?,operation=?,description=?,service_name=?,source_version=?,status='active',last_discovered_at=?,updated_at=?,updated_by=? WHERE id=? AND deleted_at IS NULL`
		_, err = tx.ExecContext(ctx, tx.Rebind(query), route.Protocol, route.Method, route.Path, route.Operation, route.Description, route.ServiceName, route.SourceVersion, now, now, actor, route.ID)
	}
	if err != nil {
		return fmt.Errorf("upsert route %q: %w", route.Path, err)
	}
	return nil
}
