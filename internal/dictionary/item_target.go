package dictionary

import (
	"context"
	"encoding/json"
)

type ItemUpdateTarget struct {
	ID           string          `json:"id"`
	DictionaryID string          `json:"dictionary_id"`
	ParentID     *string         `json:"parent_id"`
	ParentName   string          `json:"parent_name"`
	Code         string          `json:"code"`
	Name         string          `json:"name"`
	Value        string          `json:"value"`
	Disabled     bool            `json:"disabled"`
	SortOrder    int64           `json:"sort_order"`
	Extension    json.RawMessage `json:"extension" swaggertype:"object"`
	Version      int64           `json:"version"`
}

type ItemDeleteTarget struct {
	ID           string `json:"id"`
	DictionaryID string `json:"dictionary_id"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	Version      int64  `json:"version"`
}

func (s *Service) ItemUpdateTarget(ctx context.Context, id string) (ItemUpdateTarget, error) {
	item, err := s.GetItem(ctx, id)
	if err != nil {
		return ItemUpdateTarget{}, err
	}
	return ItemUpdateTarget{
		ID: item.ID, DictionaryID: item.DictionaryID,
		ParentID: item.ParentID, ParentName: item.ParentName,
		Code: item.Code, Name: item.Name, Value: item.Value,
		Disabled: item.Disabled, SortOrder: item.SortOrder,
		Extension: item.Extension, Version: item.Version,
	}, nil
}

func (s *Service) ItemDeleteTarget(ctx context.Context, id string) (ItemDeleteTarget, error) {
	item, err := s.GetItem(ctx, id)
	if err != nil {
		return ItemDeleteTarget{}, err
	}
	return ItemDeleteTarget{
		ID: item.ID, DictionaryID: item.DictionaryID,
		Code: item.Code, Name: item.Name, Version: item.Version,
	}, nil
}
