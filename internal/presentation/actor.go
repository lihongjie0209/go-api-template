package presentation

import (
	"context"
	"errors"
	"strings"

	"github.com/jmoiron/sqlx"
)

const maxActorIDs = 1000

var ErrInvalidActorIDs = errors.New("invalid actor ids")

// ActorNames resolves identities owned by this service in one bounded query.
// Missing, deleted, system and external identities deliberately fall back to
// their stable ID so response records never expose an opaque ID without a
// display field.
func ActorNames(ctx context.Context, db *sqlx.DB, ids ...string) (map[string]string, error) {
	names := make(map[string]string, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if len(id) > 256 {
			return nil, ErrInvalidActorIDs
		}
		if _, exists := names[id]; exists {
			continue
		}
		names[id] = id
		unique = append(unique, id)
		if len(unique) > maxActorIDs {
			return nil, ErrInvalidActorIDs
		}
	}
	if len(unique) == 0 {
		return names, nil
	}
	if db == nil {
		return nil, errors.New("actor display database is unavailable")
	}
	query, args, err := sqlx.In(`SELECT id,display_name FROM identity_users WHERE deleted_at IS NULL AND id IN (?)
UNION ALL SELECT id,name AS display_name FROM identity_service_accounts WHERE deleted_at IS NULL AND id IN (?)`, unique, unique)
	if err != nil {
		return nil, err
	}
	rows := []struct {
		ID          string `db:"id"`
		DisplayName string `db:"display_name"`
	}{}
	if err := db.SelectContext(ctx, &rows, db.Rebind(query), args...); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if name := strings.TrimSpace(row.DisplayName); name != "" {
			names[row.ID] = name
		}
	}
	return names, nil
}
