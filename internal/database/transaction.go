package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var ErrMissingAuditActor = errors.New("database transaction requires an audit actor in context")

type Transactor struct{ db *sqlx.DB }

func NewTransactor(db *sqlx.DB) *Transactor { return &Transactor{db: db} }

func (t *Transactor) Available() bool { return t.db != nil }

func (t *Transactor) Within(ctx context.Context, opts *sql.TxOptions, fn func(*sqlx.Tx) error) (runErr error) {
	if t.db == nil {
		return fmt.Errorf("begin transaction: database is disabled")
	}
	tx, err := t.db.BeginTxx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	committed := false
	mysqlActorSet := false
	defer func() {
		if mysqlActorSet {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			if _, clearErr := tx.ExecContext(cleanupCtx, "SET @app_actor_id = NULL"); clearErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("clear transaction audit actor: %w", clearErr))
			}
			cancel()
		}
		if !committed {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				runErr = errors.Join(runErr, fmt.Errorf("rollback transaction: %w", rollbackErr))
			}
		}
	}()
	if err := setAuditActor(ctx, tx, t.db.DriverName()); err != nil {
		return err
	}
	mysqlActorSet = t.db.DriverName() == "mysql"
	if err := fn(tx); err != nil {
		return err
	}
	if mysqlActorSet {
		if _, err := tx.ExecContext(ctx, "SET @app_actor_id = NULL"); err != nil {
			return fmt.Errorf("clear transaction audit actor: %w", err)
		}
		mysqlActorSet = false
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}

func setAuditActor(ctx context.Context, tx *sqlx.Tx, driver string) error {
	actor, ok := platformprincipal.FromContext(ctx)
	if !ok || strings.TrimSpace(actor.ID) == "" {
		return ErrMissingAuditActor
	}
	if driver == "mysql" {
		if _, err := tx.ExecContext(ctx, "SET @app_actor_id = ?", actor.ID); err != nil {
			return fmt.Errorf("set transaction audit actor: %w", err)
		}
		return nil
	}
	if driver != "pgx" && driver != "postgres" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.actor_id', $1, true)`, actor.ID); err != nil {
		return fmt.Errorf("set transaction audit actor: %w", err)
	}
	return nil
}
