package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var ErrMissingAuditActor = errors.New("database transaction requires an audit actor in context")

type Transactor struct{ db *sqlx.DB }

func NewTransactor(db *sqlx.DB) *Transactor { return &Transactor{db: db} }

func (t *Transactor) Available() bool { return t.db != nil }

func (t *Transactor) Within(ctx context.Context, opts *sql.TxOptions, fn func(*sqlx.Tx) error) error {
	if t.db == nil {
		return fmt.Errorf("begin transaction: database is disabled")
	}
	tx, err := t.db.BeginTxx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := setAuditActor(ctx, tx, t.db.DriverName()); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := fn(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return fmt.Errorf("rollback transaction after %v: %w", err, rollbackErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func setAuditActor(ctx context.Context, tx *sqlx.Tx, driver string) error {
	actor, ok := platformprincipal.FromContext(ctx)
	if !ok || strings.TrimSpace(actor.ID) == "" {
		return ErrMissingAuditActor
	}
	if driver != "pgx" && driver != "postgres" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.actor_id', $1, true)`, actor.ID); err != nil {
		return fmt.Errorf("set transaction audit actor: %w", err)
	}
	return nil
}
