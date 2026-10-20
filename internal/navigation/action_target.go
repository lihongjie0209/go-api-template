package navigation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type UpdateTarget struct {
	ID              string          `db:"id" json:"id"`
	ApplicationID   string          `db:"application_id" json:"application_id"`
	ApplicationName string          `db:"application_name" json:"application_name"`
	ParentID        *string         `db:"parent_id" json:"parent_id"`
	ParentName      string          `db:"parent_name" json:"parent_name"`
	Key             string          `db:"navigation_key" json:"navigation_key"`
	Name            string          `db:"name" json:"name"`
	Type            string          `db:"navigation_type" json:"navigation_type"`
	RoutePath       string          `db:"route_path" json:"route_path"`
	Component       string          `db:"component" json:"component"`
	Icon            string          `db:"icon" json:"icon"`
	Resource        string          `db:"resource" json:"resource"`
	Action          string          `db:"action" json:"action"`
	Visible         bool            `db:"visible" json:"visible"`
	Status          string          `db:"status" json:"status"`
	SortOrder       int64           `db:"sort_order" json:"sort_order"`
	Metadata        json.RawMessage `db:"metadata" json:"metadata" swaggertype:"object"`
	Version         int64           `db:"version" json:"version"`
}

type DeleteTarget struct {
	ID              string `db:"id" json:"id"`
	ApplicationID   string `db:"application_id" json:"application_id"`
	ApplicationName string `db:"application_name" json:"application_name"`
	Key             string `db:"navigation_key" json:"navigation_key"`
	Name            string `db:"name" json:"name"`
	Type            string `db:"navigation_type" json:"navigation_type"`
	Version         int64  `db:"version" json:"version"`
}

const targetFrom = ` FROM navigations n JOIN applications a ON a.id=n.application_id AND a.deleted_at IS NULL LEFT JOIN navigations parent ON parent.id=n.parent_id AND parent.application_id=n.application_id AND parent.deleted_at IS NULL WHERE n.id=? AND n.deleted_at IS NULL`

func (s *Service) UpdateTarget(ctx context.Context, id string) (UpdateTarget, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return UpdateTarget{}, err
	}
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return UpdateTarget{}, ErrInvalid
	}
	id = parsed.String()
	const fields = `n.id,n.application_id,a.name AS application_name,n.parent_id,COALESCE(parent.name,'') AS parent_name,n.navigation_key,n.name,n.navigation_type,n.route_path,n.component,n.icon,n.resource,n.action,n.visible,n.status,n.sort_order,n.metadata,n.version`
	var target UpdateTarget
	err = s.db.GetContext(ctx, &target, s.db.Rebind(`SELECT `+fields+targetFrom), id)
	if errors.Is(err, sql.ErrNoRows) {
		return UpdateTarget{}, ErrNotFound
	}
	return target, err
}

func (s *Service) DeleteTarget(ctx context.Context, id string) (DeleteTarget, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return DeleteTarget{}, err
	}
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return DeleteTarget{}, ErrInvalid
	}
	id = parsed.String()
	const fields = `n.id,n.application_id,a.name AS application_name,n.navigation_key,n.name,n.navigation_type,n.version`
	var target DeleteTarget
	err = s.db.GetContext(ctx, &target, s.db.Rebind(`SELECT `+fields+targetFrom), id)
	if errors.Is(err, sql.ErrNoRows) {
		return DeleteTarget{}, ErrNotFound
	}
	return target, err
}
