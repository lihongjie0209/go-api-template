package resourcecatalog

import (
	"context"

	"github.com/lihongjie0209/go-api-template/internal/presentation"
)

func (s *Service) presentResources(ctx context.Context, rows []Resource) error {
	ids := make([]string, 0, len(rows)*2)
	for _, row := range rows {
		ids = append(ids, row.CreatedBy, row.UpdatedBy)
	}
	names, err := presentation.ActorNames(ctx, s.actors, ids...)
	if err != nil {
		return err
	}
	for index := range rows {
		rows[index].CreatedByName = names[rows[index].CreatedBy]
		rows[index].UpdatedByName = names[rows[index].UpdatedBy]
		presentResourceTimes(&rows[index])
	}
	return nil
}

func (s *Service) presentActions(ctx context.Context, rows []Action) error {
	ids := make([]string, 0, len(rows)*2)
	for _, row := range rows {
		ids = append(ids, row.CreatedBy, row.UpdatedBy)
	}
	names, err := presentation.ActorNames(ctx, s.actors, ids...)
	if err != nil {
		return err
	}
	for index := range rows {
		rows[index].CreatedByName = names[rows[index].CreatedBy]
		rows[index].UpdatedByName = names[rows[index].UpdatedBy]
		presentActionTimes(&rows[index])
	}
	return nil
}

// A response-time Identity failure after COMMIT must not make a successful
// mutation look retryable. Stable actor IDs remain usable display fallbacks.
func (s *Service) presentResourceMutation(ctx context.Context, row Resource) Resource {
	rows := []Resource{row}
	if err := s.presentResources(ctx, rows); err == nil {
		return rows[0]
	}
	row.CreatedByName, row.UpdatedByName = row.CreatedBy, row.UpdatedBy
	presentResourceTimes(&row)
	return row
}

func (s *Service) presentActionMutation(ctx context.Context, row Action) Action {
	rows := []Action{row}
	if err := s.presentActions(ctx, rows); err == nil {
		return rows[0]
	}
	row.CreatedByName, row.UpdatedByName = row.CreatedBy, row.UpdatedBy
	presentActionTimes(&row)
	return row
}

func presentResourceTimes(row *Resource) {
	row.CreatedAt = presentation.Time(row.CreatedAt)
	row.UpdatedAt = presentation.Time(row.UpdatedAt)
}

func presentActionTimes(row *Action) {
	row.CreatedAt = presentation.Time(row.CreatedAt)
	row.UpdatedAt = presentation.Time(row.UpdatedAt)
}
