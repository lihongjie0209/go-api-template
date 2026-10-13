// Package resourcecatalog persists the backend-owned Resource/Action catalog.
// The code registry is authoritative for source=code; other sources are never
// changed by startup reconciliation.
package resourcecatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/lihongjie0209/microservice-platform-go/stableid"
)

var (
	ErrSourceConflict = errors.New("resource catalog: code key belongs to another source")
	ErrStaleBuild     = errors.New("resource catalog: older build cannot overwrite a newer catalog")
)

const (
	// Namespace is immutable across deployments. Its UUIDv5 values are stable
	// identities for code-owned resource and action records.
	Namespace = "308ae342-3c2c-4f7e-b786-f6323730fe76"
	codeActor = "system:resource-catalog"
)

type Syncer struct {
	db         *sqlx.DB
	transactor *database.Transactor
	registry   *resourceaction.Registry
}

func New(db *sqlx.DB, transactor *database.Transactor, registry *resourceaction.Registry) *Syncer {
	return &Syncer{db: db, transactor: transactor, registry: registry}
}

type resourceRow struct {
	ID          string       `db:"id"`
	Name        string       `db:"name"`
	Description string       `db:"description"`
	Scope       string       `db:"scope"`
	Source      string       `db:"source"`
	Status      string       `db:"status"`
	DeletedAt   sql.NullTime `db:"deleted_at"`
}

type actionRow struct {
	ID          string       `db:"id"`
	Name        string       `db:"name"`
	Description string       `db:"description"`
	Source      string       `db:"source"`
	Status      string       `db:"status"`
	DeletedAt   sql.NullTime `db:"deleted_at"`
}

type revisionRow struct {
	BuildTime string `db:"build_time"`
	CommitID  string `db:"commit_id"`
}

// Sync atomically reconciles every code-owned record before transports start.
// The audit actor and the serialized source row are held by one transaction.
func (s *Syncer) Sync(ctx context.Context, buildTime, commit string) error {
	if s == nil || s.db == nil || s.transactor == nil || s.registry == nil {
		return errors.New("resource catalog: database and registry are required")
	}
	definitions := s.registry.Definitions()
	generator, err := stableid.New(Namespace)
	if err != nil {
		return fmt.Errorf("configure resource catalog namespace: %w", err)
	}
	actorCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: codeActor, Type: platformprincipal.TypeSystem})
	return s.transactor.Within(actorCtx, nil, func(tx *sqlx.Tx) error {
		var current revisionRow
		if err := tx.GetContext(actorCtx, &current, `SELECT build_time,commit_id FROM authorization_catalog_syncs WHERE source='code' AND deleted_at IS NULL FOR UPDATE`); err != nil {
			return fmt.Errorf("lock code catalog revision: %w", err)
		}
		if err := checkBuild(current, revisionRow{BuildTime: buildTime, CommitID: commit}); err != nil {
			return err
		}
		resourceIDs := make([]string, 0, len(definitions))
		actionIDs := make([]string, 0)
		for _, definition := range definitions {
			id, err := generator.String("resource:" + definition.Key)
			if err != nil {
				return err
			}
			if err := syncResource(actorCtx, tx, id, definition); err != nil {
				return err
			}
			resourceIDs = append(resourceIDs, id)
			for _, action := range definition.Actions {
				actionID, err := generator.String("action:" + definition.Key + ":" + action.Key)
				if err != nil {
					return err
				}
				if err := syncAction(actorCtx, tx, actionID, id, action); err != nil {
					return err
				}
				actionIDs = append(actionIDs, actionID)
			}
		}
		if err := disableMissing(actorCtx, tx, "authorization_resource_actions", actionIDs); err != nil {
			return err
		}
		if err := disableMissing(actorCtx, tx, "authorization_resources", resourceIDs); err != nil {
			return err
		}
		if err := backfillLegacyPermissionLinks(actorCtx, tx, generator); err != nil {
			return err
		}
		if current.BuildTime != buildTime || current.CommitID != commit {
			_, err := tx.ExecContext(actorCtx, tx.Rebind(`UPDATE authorization_catalog_syncs SET build_time=?,commit_id=? WHERE source='code' AND deleted_at IS NULL`), buildTime, commit)
			if err != nil {
				return fmt.Errorf("update code catalog revision: %w", err)
			}
		}
		return nil
	})
}

// Existing one-to-one permission definitions are migrated after the code
// catalog has been reconciled. The link is then authoritative for decisions.
func backfillLegacyPermissionLinks(ctx context.Context, tx *sqlx.Tx, generator stableid.Generator) error {
	var marker string
	err := tx.GetContext(ctx, &marker, `SELECT source FROM authorization_catalog_syncs WHERE source='legacy-permissions' AND deleted_at IS NULL`)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows := []struct {
		PermissionID string `db:"permission_id"`
		ActionID     string `db:"action_id"`
	}{}
	err = tx.SelectContext(ctx, &rows, `SELECT p.id AS permission_id,a.id AS action_id FROM permissions p
		JOIN authorization_resources r ON r.resource_key=p.resource AND r.deleted_at IS NULL
		JOIN authorization_resource_actions a ON a.resource_id=r.id AND a.action_key=p.action AND a.deleted_at IS NULL
		WHERE p.node_type='permission' AND p.deleted_at IS NULL AND p.resource<>'' AND p.action<>''`)
	if err != nil {
		return fmt.Errorf("load legacy permission links: %w", err)
	}
	for _, row := range rows {
		var existing struct {
			ID        string       `db:"id"`
			DeletedAt sql.NullTime `db:"deleted_at"`
		}
		err := tx.GetContext(ctx, &existing, tx.Rebind(`SELECT id,deleted_at FROM permission_resource_actions WHERE permission_id=? AND action_id=? FOR UPDATE`), row.PermissionID, row.ActionID)
		if err == nil {
			if existing.DeletedAt.Valid {
				if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE permission_resource_actions SET deleted_at=NULL,deleted_by=NULL WHERE id=?`), existing.ID); err != nil {
					return err
				}
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		id, err := generator.String("legacy-permission:" + row.PermissionID + ":" + row.ActionID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, tx.Rebind(`INSERT INTO permission_resource_actions (id,permission_id,action_id,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,?,?,1)`), id, row.PermissionID, row.ActionID, time.Now(), codeActor, time.Now(), codeActor)
		if err != nil {
			return fmt.Errorf("backfill permission link: %w", err)
		}
	}
	_, err = tx.ExecContext(ctx, tx.Rebind(`INSERT INTO authorization_catalog_syncs (source,build_time,commit_id,created_at,created_by,updated_at,updated_by,version) VALUES ('legacy-permissions','','',?,?,?,?,1)`), time.Now(), codeActor, time.Now(), codeActor)
	return err
}

func checkBuild(stored, incoming revisionRow) error {
	previous, previousErr := time.Parse(time.RFC3339, stored.BuildTime)
	if previousErr != nil {
		return nil // First deployment or a local development build.
	}
	next, nextErr := time.Parse(time.RFC3339, incoming.BuildTime)
	if nextErr != nil || next.Before(previous) || next.Equal(previous) && incoming.CommitID != stored.CommitID {
		return ErrStaleBuild
	}
	return nil
}

func syncResource(ctx context.Context, tx *sqlx.Tx, id string, definition resourceaction.ResourceDefinition) error {
	var row resourceRow
	err := tx.GetContext(ctx, &row, tx.Rebind(`SELECT id,name,description,scope,source,status,deleted_at FROM authorization_resources WHERE resource_key=? FOR UPDATE`), definition.Key)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, tx.Rebind(`INSERT INTO authorization_resources (id,resource_key,name,description,scope,source,status,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,'code','active',?,?,?,?,1)`), id, definition.Key, definition.Name, definition.Description, definition.Scope, time.Now(), codeActor, time.Now(), codeActor)
		if err != nil {
			return fmt.Errorf("insert resource %s: %w", definition.Key, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("load resource %s: %w", definition.Key, err)
	}
	if row.Source != "code" || row.ID != id {
		return fmt.Errorf("%w: resource %s", ErrSourceConflict, definition.Key)
	}
	if row.Name == definition.Name && row.Description == definition.Description && row.Scope == string(definition.Scope) && row.Status == "active" && !row.DeletedAt.Valid {
		return nil
	}
	_, err = tx.ExecContext(ctx, tx.Rebind(`UPDATE authorization_resources SET name=?,description=?,scope=?,status='active',deleted_at=NULL,deleted_by=NULL WHERE id=? AND source='code'`), definition.Name, definition.Description, definition.Scope, id)
	if err != nil {
		return fmt.Errorf("update resource %s: %w", definition.Key, err)
	}
	return nil
}

func syncAction(ctx context.Context, tx *sqlx.Tx, id, resourceID string, definition resourceaction.ActionDefinition) error {
	var row actionRow
	err := tx.GetContext(ctx, &row, tx.Rebind(`SELECT id,name,description,source,status,deleted_at FROM authorization_resource_actions WHERE resource_id=? AND action_key=? FOR UPDATE`), resourceID, definition.Key)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, tx.Rebind(`INSERT INTO authorization_resource_actions (id,resource_id,action_key,name,description,source,status,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,'code','active',?,?,?,?,1)`), id, resourceID, definition.Key, definition.Name, definition.Description, time.Now(), codeActor, time.Now(), codeActor)
		if err != nil {
			return fmt.Errorf("insert action %s: %w", definition.Key, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("load action %s: %w", definition.Key, err)
	}
	if row.Source != "code" || row.ID != id {
		return fmt.Errorf("%w: action %s", ErrSourceConflict, definition.Key)
	}
	if row.Name == definition.Name && row.Description == definition.Description && row.Status == "active" && !row.DeletedAt.Valid {
		return nil
	}
	_, err = tx.ExecContext(ctx, tx.Rebind(`UPDATE authorization_resource_actions SET name=?,description=?,status='active',deleted_at=NULL,deleted_by=NULL WHERE id=? AND source='code'`), definition.Name, definition.Description, id)
	if err != nil {
		return fmt.Errorf("update action %s: %w", definition.Key, err)
	}
	return nil
}

func disableMissing(ctx context.Context, tx *sqlx.Tx, table string, retained []string) error {
	// The table name is selected only from these two constants, never from input.
	if table != "authorization_resources" && table != "authorization_resource_actions" {
		return errors.New("resource catalog: invalid sync table")
	}
	query := `UPDATE ` + table + ` SET status='disabled' WHERE source='code' AND status<>'disabled' AND deleted_at IS NULL`
	args := []any{}
	if len(retained) > 0 {
		var err error
		query, args, err = sqlx.In(query+` AND id NOT IN (?)`, retained)
		if err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, tx.Rebind(query), args...); err != nil {
		return fmt.Errorf("disable removed %s: %w", table, err)
	}
	return nil
}
