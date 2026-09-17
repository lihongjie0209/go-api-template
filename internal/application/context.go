package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var ErrContextNotFound = errors.New("current application context not found")

type Context struct {
	ID              string    `db:"id" json:"id"`
	ApplicationID   string    `db:"application_id" json:"application_id"`
	ApplicationCode string    `db:"application_code" json:"application_code"`
	ApplicationName string    `db:"application_name" json:"application_name"`
	Icon            string    `db:"icon" json:"icon"`
	HomePath        string    `db:"home_path" json:"home_path"`
	CreatedAt       time.Time `db:"created_at" json:"created_at"`
	CreatedBy       string    `db:"created_by" json:"created_by"`
	UpdatedAt       time.Time `db:"updated_at" json:"updated_at"`
	UpdatedBy       string    `db:"updated_by" json:"updated_by"`
	Version         int64     `db:"version" json:"version"`
}

type SwitchInput struct {
	ApplicationID string
	Version       int64
}

const contextColumns = `c.id,c.application_id,a.code application_code,a.name application_name,a.icon,a.home_path,c.created_at,c.created_by,c.updated_at,c.updated_by,c.version`

func (s *TenantAccessService) CurrentContext(ctx context.Context) (Context, error) {
	actor, err := currentApplicationActor(ctx)
	if err != nil {
		return Context{}, err
	}
	var result Context
	query := `SELECT ` + contextColumns + ` FROM application_session_contexts c JOIN identity_sessions s ON s.id=c.session_id AND s.user_id=c.user_id AND s.revoked_at IS NULL AND s.expires_at>? AND s.deleted_at IS NULL JOIN tenant_memberships m ON m.id=c.membership_id AND m.tenant_id=c.tenant_id AND m.user_id=c.user_id AND m.status='active' AND m.deleted_at IS NULL JOIN tenant_application_grants g ON g.tenant_id=c.tenant_id AND g.application_id=c.application_id AND g.status='active' AND g.deleted_at IS NULL AND (g.starts_at IS NULL OR g.starts_at<=?) AND (g.expires_at IS NULL OR g.expires_at>?) JOIN applications a ON a.id=c.application_id AND a.status='active' AND a.deleted_at IS NULL WHERE c.session_id=? AND c.tenant_id=? AND c.membership_id=? AND c.user_id=? AND c.deleted_at IS NULL`
	now := time.Now()
	err = s.db.GetContext(ctx, &result, s.db.Rebind(query), now, now, now, actor.SessionID, actor.TenantID, actor.MembershipID, actor.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return Context{}, ErrContextNotFound
	}
	if err != nil {
		return Context{}, err
	}
	return presentContext(result), nil
}

func (s *TenantAccessService) SwitchContext(ctx context.Context, input SwitchInput) (Context, error) {
	actor, err := currentApplicationActor(ctx)
	input.ApplicationID = strings.TrimSpace(input.ApplicationID)
	if err != nil || input.ApplicationID == "" || len(input.ApplicationID) > 128 || input.Version < 0 {
		return Context{}, ErrGrantInvalid
	}
	id := uuid.NewString()
	started := time.Now()
	operation := operationlog.Entry{Operation: "application.context.switch", ResourceType: "application", ResourceID: input.ApplicationID, Source: "backend", Protocol: "service", Request: map[string]any{"application_id": input.ApplicationID, "version": input.Version}}
	security := securitylog.Entry{EventType: securitylog.EventApplicationContextSwitch, SubjectID: actor.ID, SubjectType: string(actor.Type), TenantID: actor.TenantID, SessionID: actor.SessionID, Metadata: map[string]any{"application_id": input.ApplicationID}}
	err = s.tx.Within(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sqlx.Tx) error {
		var applicationName string
		now := time.Now()
		allowed := `SELECT a.name FROM applications a JOIN tenant_application_grants g ON g.application_id=a.id AND g.tenant_id=? AND g.status='active' AND g.deleted_at IS NULL AND (g.starts_at IS NULL OR g.starts_at<=?) AND (g.expires_at IS NULL OR g.expires_at>?) JOIN tenant_memberships m ON m.tenant_id=g.tenant_id AND m.id=? AND m.user_id=? AND m.status='active' AND m.deleted_at IS NULL JOIN identity_sessions s ON s.id=? AND s.user_id=m.user_id AND s.revoked_at IS NULL AND s.expires_at>? AND s.deleted_at IS NULL WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL`
		if err := tx.GetContext(ctx, &applicationName, tx.Rebind(allowed), actor.TenantID, now, now, actor.MembershipID, actor.ID, actor.SessionID, now, input.ApplicationID); errors.Is(err, sql.ErrNoRows) {
			return ErrGrantForbidden
		} else if err != nil {
			return err
		}
		var existing struct {
			ID      string `db:"id"`
			Version int64  `db:"version"`
		}
		err := tx.GetContext(ctx, &existing, tx.Rebind(`SELECT id,version FROM application_session_contexts WHERE session_id=? AND tenant_id=? AND deleted_at IS NULL FOR UPDATE`), actor.SessionID, actor.TenantID)
		switch {
		case errors.Is(err, sql.ErrNoRows) && input.Version == 0:
			if _, err := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO application_session_contexts(id,session_id,user_id,tenant_id,membership_id,application_id,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,?,1)`), id, actor.SessionID, actor.ID, actor.TenantID, actor.MembershipID, input.ApplicationID, now, actor.ID, now, actor.ID); err != nil {
				return err
			}
		case errors.Is(err, sql.ErrNoRows):
			return ErrGrantConflict
		case err != nil:
			return err
		case input.Version != existing.Version:
			return ErrGrantConflict
		default:
			id = existing.ID
			result, updateErr := tx.ExecContext(ctx, tx.Rebind(`UPDATE application_session_contexts SET user_id=?,membership_id=?,application_id=?,updated_at=?,updated_by=? WHERE id=? AND session_id=? AND tenant_id=? AND version=? AND deleted_at IS NULL`), actor.ID, actor.MembershipID, input.ApplicationID, now, actor.ID, id, actor.SessionID, actor.TenantID, input.Version)
			if updateErr != nil {
				return updateErr
			}
			affected, updateErr := result.RowsAffected()
			if updateErr != nil {
				return updateErr
			}
			if affected != 1 {
				return ErrGrantConflict
			}
		}
		operation.ResourceName, operation.Duration, operation.Succeeded = applicationName, time.Since(started), true
		security.Succeeded = true
		if s.operations != nil {
			if err := s.operations.RecordTx(ctx, tx, operation); err != nil {
				return err
			}
		}
		if s.security != nil {
			return s.security.RecordTx(ctx, tx, security)
		}
		return nil
	})
	if err != nil {
		operation.Duration, operation.ErrorCode, operation.ErrorMessage = time.Since(started), "operation_failed", "operation failed"
		security.ErrorCode, security.ErrorMessage = "application_context_switch_failed", "application context switch failed"
		if s.operations != nil {
			_ = s.operations.Record(ctx, operation)
		}
		if s.security != nil {
			if recordErr := s.security.Record(ctx, security); recordErr != nil && s.security.FailClosed() {
				return Context{}, fmt.Errorf("%w: %v", err, recordErr)
			}
		}
		return Context{}, err
	}
	return s.CurrentContext(ctx)
}

func currentApplicationActor(ctx context.Context) (platformprincipal.Principal, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.Type != platformprincipal.TypeUser || actor.ID == "" || actor.SessionID == "" || actor.TenantID == "" || actor.MembershipID == "" {
		return platformprincipal.Principal{}, ErrGrantForbidden
	}
	return actor, nil
}

func presentContext(value Context) Context {
	value.CreatedAt = presentation.Time(value.CreatedAt)
	value.UpdatedAt = presentation.Time(value.UpdatedAt)
	return value
}
