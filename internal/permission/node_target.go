package permission

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

type NodeUpdateTarget struct {
	ID            string  `json:"id"`
	ParentID      *string `json:"parent_id"`
	ParentName    string  `json:"parent_name"`
	PermissionKey string  `json:"permission_key"`
	Name          string  `json:"name"`
	NodeType      string  `json:"node_type"`
	Resource      string  `json:"resource"`
	Action        string  `json:"action"`
	Description   string  `json:"description"`
	SortOrder     int64   `json:"sort_order"`
	Status        string  `json:"status"`
	HasChildren   bool    `json:"has_children"`
	Version       int64   `json:"version"`
}

type NodeDeleteTarget struct {
	ID            string `json:"id"`
	PermissionKey string `json:"permission_key"`
	Name          string `json:"name"`
	NodeType      string `json:"node_type"`
	ParentName    string `json:"parent_name"`
	Version       int64  `json:"version"`
}

func (s *Service) NodeUpdateTarget(ctx context.Context, id string) (NodeUpdateTarget, error) {
	if _, err := uuid.Parse(id); err != nil {
		return NodeUpdateTarget{}, ErrInvalid
	}
	record, err := s.repository.Get(ctx, id)
	if err != nil {
		return NodeUpdateTarget{}, err
	}
	if record.IsSystem {
		return NodeUpdateTarget{}, ErrConflict
	}
	var children int64
	err = s.repository.db.GetContext(ctx, &children, s.repository.db.Rebind(`SELECT count(*) FROM permissions WHERE parent_id=? AND deleted_at IS NULL`), id)
	if err != nil {
		return NodeUpdateTarget{}, fmt.Errorf("count permission children: %w", err)
	}
	return NodeUpdateTarget{
		ID: record.ID, ParentID: record.ParentID, ParentName: record.ParentName,
		PermissionKey: record.Key, Name: record.Name, NodeType: record.NodeType,
		Resource: record.Resource, Action: record.Action, Description: record.Description,
		SortOrder: record.SortOrder, Status: record.Status, HasChildren: children > 0,
		Version: record.Version,
	}, nil
}

func (s *Service) NodeDeleteTarget(ctx context.Context, id string) (NodeDeleteTarget, error) {
	if _, err := uuid.Parse(id); err != nil {
		return NodeDeleteTarget{}, ErrInvalid
	}
	record, err := s.repository.Get(ctx, id)
	if err != nil {
		return NodeDeleteTarget{}, err
	}
	if record.IsSystem {
		return NodeDeleteTarget{}, ErrConflict
	}
	return NodeDeleteTarget{
		ID: record.ID, PermissionKey: record.Key, Name: record.Name,
		NodeType: record.NodeType, ParentName: record.ParentName,
		Version: record.Version,
	}, nil
}
