package resourcecatalog

import (
	"context"

	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type ResourceUpdateTarget struct {
	ID          string                       `json:"id"`
	Key         string                       `json:"resource_key"`
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Scope       resourceaction.ResourceScope `json:"scope"`
	Status      string                       `json:"status"`
	Version     int64                        `json:"version"`
}

type ResourceDeleteTarget struct {
	ID      string `json:"id"`
	Key     string `json:"resource_key"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Version int64  `json:"version"`
}

type ActionUpdateTarget struct {
	ID           string `json:"id"`
	ResourceID   string `json:"resource_id"`
	ResourceKey  string `json:"resource_key"`
	ResourceName string `json:"resource_name"`
	Key          string `json:"action_key"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	Version      int64  `json:"version"`
}

type ActionDeleteTarget struct {
	ID           string `json:"id"`
	ResourceKey  string `json:"resource_key"`
	ResourceName string `json:"resource_name"`
	Key          string `json:"action_key"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Version      int64  `json:"version"`
}

func (s *Service) ResourceUpdateTarget(ctx context.Context, id string) (ResourceUpdateTarget, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return ResourceUpdateTarget{}, err
	}
	row, err := s.GetResource(ctx, id)
	if err != nil {
		return ResourceUpdateTarget{}, err
	}
	if row.Source != "manual" {
		return ResourceUpdateTarget{}, ErrCodeOwned
	}
	return ResourceUpdateTarget{ID: row.ID, Key: row.Key, Name: row.Name, Description: row.Description, Scope: row.Scope, Status: row.Status, Version: row.Version}, nil
}

func (s *Service) ResourceDeleteTarget(ctx context.Context, id string) (ResourceDeleteTarget, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return ResourceDeleteTarget{}, err
	}
	row, err := s.GetResource(ctx, id)
	if err != nil {
		return ResourceDeleteTarget{}, err
	}
	if row.Source != "manual" {
		return ResourceDeleteTarget{}, ErrCodeOwned
	}
	return ResourceDeleteTarget{ID: row.ID, Key: row.Key, Name: row.Name, Status: row.Status, Version: row.Version}, nil
}

func (s *Service) ActionUpdateTarget(ctx context.Context, id string) (ActionUpdateTarget, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return ActionUpdateTarget{}, err
	}
	row, err := s.GetAction(ctx, id)
	if err != nil {
		return ActionUpdateTarget{}, err
	}
	if row.Source != "manual" {
		return ActionUpdateTarget{}, ErrCodeOwned
	}
	return ActionUpdateTarget{ID: row.ID, ResourceID: row.ResourceID, ResourceKey: row.ResourceKey, ResourceName: row.ResourceName, Key: row.Key, Name: row.Name, Description: row.Description, Status: row.Status, Version: row.Version}, nil
}

func (s *Service) ActionDeleteTarget(ctx context.Context, id string) (ActionDeleteTarget, error) {
	if _, err := platformprincipal.Require(ctx); err != nil {
		return ActionDeleteTarget{}, err
	}
	row, err := s.GetAction(ctx, id)
	if err != nil {
		return ActionDeleteTarget{}, err
	}
	if row.Source != "manual" {
		return ActionDeleteTarget{}, ErrCodeOwned
	}
	return ActionDeleteTarget{ID: row.ID, ResourceKey: row.ResourceKey, ResourceName: row.ResourceName, Key: row.Key, Name: row.Name, Status: row.Status, Version: row.Version}, nil
}
