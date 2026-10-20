package dictionary

import (
	"context"
	"encoding/json"
)

type UpdateTarget struct {
	ID          string          `json:"id"`
	Code        string          `json:"code"`
	Name        string          `json:"name"`
	Kind        Kind            `json:"type"`
	Source      Source          `json:"source"`
	Description string          `json:"description"`
	Status      string          `json:"status"`
	Extension   json.RawMessage `json:"extension" swaggertype:"object"`
	Version     int64           `json:"version"`
}

type DeleteTarget struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Name    string `json:"name"`
	Kind    Kind   `json:"type"`
	Source  Source `json:"source"`
	Status  string `json:"status"`
	Version int64  `json:"version"`
}

func (s *Service) UpdateTarget(ctx context.Context, id string) (UpdateTarget, error) {
	definition, err := s.GetDefinition(ctx, id)
	if err != nil {
		return UpdateTarget{}, err
	}
	return UpdateTarget{
		ID: definition.ID, Code: definition.Code, Name: definition.Name,
		Kind: definition.Kind, Source: definition.Source,
		Description: definition.Description, Status: definition.Status,
		Extension: definition.Extension, Version: definition.Version,
	}, nil
}

func (s *Service) DeleteTarget(ctx context.Context, id string) (DeleteTarget, error) {
	definition, err := s.GetDefinition(ctx, id)
	if err != nil {
		return DeleteTarget{}, err
	}
	return DeleteTarget{
		ID: definition.ID, Code: definition.Code, Name: definition.Name,
		Kind: definition.Kind, Source: definition.Source,
		Status: definition.Status, Version: definition.Version,
	}, nil
}
