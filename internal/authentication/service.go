package authentication

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/identity"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAccountLocked      = errors.New("account locked")
	ErrRefreshInvalid     = errors.New("refresh token invalid")
	ErrRefreshReused      = errors.New("refresh token reused")
	ErrSessionNotFound    = errors.New("session not found")
	ErrForbidden          = errors.New("authentication operation forbidden")
	ErrInvalid            = errors.New("invalid authentication request")
)

type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	SessionID    string `json:"session_id"`
	UserID       string `json:"-"`
}
type Credential struct {
	ID             string     `db:"id"`
	UserID         string     `db:"user_id"`
	PasswordHash   string     `db:"password_hash"`
	FailedAttempts int64      `db:"failed_attempts"`
	LockedUntil    *time.Time `db:"locked_until"`
	Version        int64      `db:"version"`
}
type session struct {
	ID                       string     `db:"id"`
	UserID                   string     `db:"user_id"`
	RefreshTokenHash         string     `db:"refresh_token_hash"`
	PreviousRefreshTokenHash string     `db:"previous_refresh_token_hash"`
	ExpiresAt                time.Time  `db:"expires_at"`
	RevokedAt                *time.Time `db:"revoked_at"`
	Version                  int64      `db:"version"`
}
type SessionView struct {
	ID           string     `db:"id" json:"id"`
	UserID       string     `db:"user_id" json:"user_id"`
	ExpiresAt    time.Time  `db:"expires_at" json:"expires_at"`
	LastSeenAt   time.Time  `db:"last_seen_at" json:"last_seen_at"`
	RevokedAt    *time.Time `db:"revoked_at" json:"revoked_at,omitempty"`
	RevokeReason string     `db:"revoke_reason" json:"revoke_reason"`
	ClientIP     string     `db:"client_ip" json:"client_ip"`
	UserAgent    string     `db:"user_agent" json:"user_agent"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
	Version      int64      `db:"version" json:"version"`
}
type SessionPage struct {
	Items    []SessionView `json:"items"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	Total    int64         `json:"total"`
}
type Service struct {
	db     *sqlx.DB
	tx     *database.Transactor
	users  *identity.Service
	jwt    *auth.Service
	hasher *auth.PasswordHasher
	cfg    config.Config
}

const identitySessionInsertSQL = `INSERT INTO identity_sessions (id,user_id,refresh_token_hash,previous_refresh_token_hash,expires_at,last_seen_at,revoke_reason,client_ip,user_agent,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?, '',?,?, '',?,?,?,?,?,?,1)`

func New(db *sqlx.DB, tx *database.Transactor, users *identity.Service, jwt *auth.Service, cfg config.Config) *Service {
	return &Service{db, tx, users, jwt, auth.NewPasswordHasher(), cfg}
}
func (s *Service) SetPassword(ctx context.Context, userID, password string) error {
	actor, ok := platformprincipal.FromContext(ctx)
	if !ok {
		return platformprincipal.ErrMissing
	}
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return err
	}
	now := time.Now()
	return s.tx.Within(ctx, nil, func(tx *sqlx.Tx) error {
		return setPasswordAndRevoke(ctx, tx, userID, hash, actor.ID, "password_reset", now)
	})
}

func (s *Service) ChangePassword(ctx context.Context, oldPassword, newPassword string) error {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.Type != platformprincipal.TypeUser {
		return ErrForbidden
	}
	hash, err := s.hasher.Hash(newPassword)
	if err != nil {
		return err
	}
	return s.tx.Within(ctx, nil, func(tx *sqlx.Tx) error {
		var credential Credential
		q := tx.Rebind(`SELECT id,user_id,password_hash,failed_attempts,locked_until,version FROM identity_user_credentials WHERE user_id=? AND deleted_at IS NULL FOR UPDATE`)
		if err := tx.GetContext(ctx, &credential, q, actor.ID); err != nil {
			return ErrInvalidCredentials
		}
		valid, verifyErr := s.hasher.Verify(oldPassword, credential.PasswordHash)
		if verifyErr != nil || !valid {
			return ErrInvalidCredentials
		}
		return updatePasswordAndRevoke(ctx, tx, credential, hash, actor.ID, "password_changed", time.Now())
	})
}

func (s *Service) Sessions(ctx context.Context, page, pageSize int) (SessionPage, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.Type != platformprincipal.TypeUser {
		return SessionPage{}, ErrForbidden
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*) FROM identity_sessions WHERE user_id=? AND deleted_at IS NULL`), actor.ID); err != nil {
		return SessionPage{}, err
	}
	items := []SessionView{}
	q := s.db.Rebind(`SELECT id,user_id,expires_at,last_seen_at,revoked_at,revoke_reason,client_ip,user_agent,created_at,version FROM identity_sessions WHERE user_id=? AND deleted_at IS NULL ORDER BY last_seen_at DESC,id LIMIT ? OFFSET ?`)
	if err := s.db.SelectContext(ctx, &items, q, actor.ID, pageSize, (page-1)*pageSize); err != nil {
		return SessionPage{}, err
	}
	return SessionPage{Items: items, Page: page, PageSize: pageSize, Total: total}, nil
}

func (s *Service) RevokeSession(ctx context.Context, sessionID string, version int64) error {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.Type != platformprincipal.TypeUser {
		return ErrForbidden
	}
	if sessionID == "" || version <= 0 {
		return ErrInvalid
	}
	return s.tx.Within(ctx, nil, func(tx *sqlx.Tx) error {
		result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE identity_sessions SET revoked_at=?,revoke_reason='user_revoked',updated_at=?,updated_by=?,version=version+1 WHERE id=? AND user_id=? AND version=? AND revoked_at IS NULL AND deleted_at IS NULL`), time.Now(), time.Now(), actor.ID, sessionID, actor.ID, version)
		if err != nil {
			return err
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("revoke session affected rows: %w", rowsErr)
		}
		if rows != 1 {
			return ErrSessionNotFound
		}
		return nil
	})
}

func (s *Service) LogoutAll(ctx context.Context) error {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.Type != platformprincipal.TypeUser {
		return ErrForbidden
	}
	return s.revokeAll(ctx, actor.ID, actor.ID, "logout_all")
}

func (s *Service) ForceLogoutAll(ctx context.Context, userID string) error {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return ErrForbidden
	}
	if userID == "" {
		return ErrInvalid
	}
	return s.revokeAll(ctx, userID, actor.ID, "forced_logout")
}

func (s *Service) revokeAll(ctx context.Context, userID, actorID, reason string) error {
	return s.tx.Within(ctx, nil, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE identity_sessions SET revoked_at=?,revoke_reason=?,updated_at=?,updated_by=?,version=version+1 WHERE user_id=? AND revoked_at IS NULL AND deleted_at IS NULL`), time.Now(), reason, time.Now(), actorID, userID)
		return err
	})
}

// AbortSession revokes a just-issued session when a fail-closed postcondition,
// such as durable security-event enqueueing, cannot be satisfied.
func (s *Service) AbortSession(ctx context.Context, sessionID, reason string) error {
	if sessionID == "" || reason == "" {
		return ErrInvalid
	}
	systemCtx := platformprincipal.SystemContext(ctx, "identity-service:session-abort")
	return s.tx.Within(systemCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		result, err := tx.ExecContext(systemCtx, tx.Rebind(`UPDATE identity_sessions SET revoked_at=?,revoke_reason=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND revoked_at IS NULL AND deleted_at IS NULL`), now, reason, now, "identity-service:session-abort", sessionID)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("abort session affected rows: %w", err)
		}
		if rows != 1 {
			return ErrSessionNotFound
		}
		return nil
	})
}

func setPasswordAndRevoke(ctx context.Context, tx *sqlx.Tx, userID, hash, actorID, reason string, now time.Time) error {
	var users int
	if err := tx.GetContext(ctx, &users, tx.Rebind(`SELECT count(*) FROM identity_users WHERE id=? AND status<>'closed' AND deleted_at IS NULL`), userID); err != nil {
		return err
	}
	if users != 1 {
		return identity.ErrNotFound
	}
	var credential Credential
	err := tx.GetContext(ctx, &credential, tx.Rebind(`SELECT id,user_id,password_hash,failed_attempts,locked_until,version FROM identity_user_credentials WHERE user_id=? AND deleted_at IS NULL FOR UPDATE`), userID)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, tx.Rebind(`INSERT INTO identity_user_credentials (id,user_id,password_hash,failed_attempts,password_changed_at,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,?,?,?,?,1)`), uuid.NewString(), userID, hash, 0, now, now, actorID, now, actorID)
		return err
	}
	if err != nil {
		return err
	}
	return updatePasswordAndRevoke(ctx, tx, credential, hash, actorID, reason, now)
}
func updatePasswordAndRevoke(ctx context.Context, tx *sqlx.Tx, credential Credential, hash, actorID, reason string, now time.Time) error {
	result, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE identity_user_credentials SET password_hash=?,failed_attempts=0,locked_until=NULL,password_changed_at=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`), hash, now, now, actorID, credential.ID, credential.Version)
	if err != nil {
		return err
	}
	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("change credential affected rows: %w", rowsErr)
	}
	if rows != 1 {
		return ErrInvalidCredentials
	}
	_, err = tx.ExecContext(ctx, tx.Rebind(`UPDATE identity_sessions SET revoked_at=?,revoke_reason=?,updated_at=?,updated_by=?,version=version+1 WHERE user_id=? AND revoked_at IS NULL AND deleted_at IS NULL`), now, reason, now, actorID, credential.UserID)
	return err
}
func (s *Service) Login(ctx context.Context, username, password, ip, ua string) (Tokens, error) {
	user, err := s.users.ResolveUsername(ctx, username)
	if err != nil || user.Status != identity.StatusActive {
		return Tokens{}, ErrInvalidCredentials
	}
	var credential Credential
	q := s.db.Rebind(`SELECT id,user_id,password_hash,failed_attempts,locked_until,version FROM identity_user_credentials WHERE user_id=? AND deleted_at IS NULL`)
	if err = s.db.GetContext(ctx, &credential, q, user.ID); err != nil {
		return Tokens{}, ErrInvalidCredentials
	}
	if credential.LockedUntil != nil && credential.LockedUntil.After(time.Now()) {
		return Tokens{}, ErrAccountLocked
	}
	valid, verifyErr := s.hasher.Verify(password, credential.PasswordHash)
	if verifyErr != nil || !valid {
		if failureErr := s.recordFailure(ctx, credential); failureErr != nil {
			return Tokens{}, fmt.Errorf("record failed login attempt: %w", failureErr)
		}
		return Tokens{}, ErrInvalidCredentials
	}
	refresh, hash, err := newRefreshToken()
	if err != nil {
		return Tokens{}, err
	}
	sessionID := uuid.NewString()
	systemCtx := platformprincipal.SystemContext(ctx, "identity-service:login")
	now := time.Now()
	err = s.tx.Within(systemCtx, nil, func(tx *sqlx.Tx) error {
		reset := tx.Rebind(`UPDATE identity_user_credentials SET failed_attempts=0,locked_until=NULL,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=?`)
		result, e := tx.ExecContext(systemCtx, reset, now, user.ID, credential.ID, credential.Version)
		if e != nil {
			return e
		}
		rows, e := result.RowsAffected()
		if e != nil {
			return fmt.Errorf("reset login failures affected rows: %w", e)
		}
		if rows != 1 {
			return ErrInvalidCredentials
		}
		insert := tx.Rebind(identitySessionInsertSQL)
		_, e = tx.ExecContext(systemCtx, insert, sessionID, user.ID, hash, now.Add(s.cfg.Authentication.RefreshTTL), now, ip, ua, now, user.ID, now, user.ID)
		return e
	})
	if err != nil {
		return Tokens{}, err
	}
	access, err := s.jwt.IssuePrincipal(platformprincipal.Principal{ID: user.ID, Type: platformprincipal.TypeUser, SessionID: sessionID})
	if err != nil {
		return Tokens{}, err
	}
	return Tokens{AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", ExpiresIn: int64(s.cfg.JWT.TTL.Seconds()), SessionID: sessionID, UserID: user.ID}, nil
}
func (s *Service) recordFailure(ctx context.Context, c Credential) error {
	systemCtx := platformprincipal.SystemContext(ctx, "identity-service:login")
	return s.tx.Within(systemCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		q := tx.Rebind(`UPDATE identity_user_credentials SET failed_attempts=failed_attempts+1,locked_until=CASE WHEN failed_attempts+1>=? THEN ? ELSE locked_until END,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND deleted_at IS NULL`)
		result, err := tx.ExecContext(systemCtx, q, s.cfg.Authentication.MaxFailedAttempts, now.Add(s.cfg.Authentication.LockDuration), now, c.UserID, c.ID)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("record login failure affected rows: %w", err)
		}
		if rows != 1 {
			return ErrInvalidCredentials
		}
		return nil
	})
}
func (s *Service) Refresh(ctx context.Context, raw string) (Tokens, error) {
	if raw == "" || len(raw) > 4096 {
		return Tokens{}, ErrInvalid
	}
	newRaw, newHash, err := newRefreshToken()
	if err != nil {
		return Tokens{}, err
	}
	oldHash := tokenHash(raw)
	systemCtx := platformprincipal.SystemContext(ctx, "identity-service:refresh")
	var current session
	reused := false
	err = s.tx.Within(systemCtx, nil, func(tx *sqlx.Tx) error {
		q := tx.Rebind(`SELECT s.id,s.user_id,s.refresh_token_hash,s.previous_refresh_token_hash,s.expires_at,s.revoked_at,s.version FROM identity_sessions s JOIN identity_users u ON u.id=s.user_id AND u.status='active' AND u.deleted_at IS NULL WHERE (s.refresh_token_hash=? OR s.previous_refresh_token_hash=?) AND s.deleted_at IS NULL FOR UPDATE`)
		if e := tx.GetContext(systemCtx, &current, q, oldHash, oldHash); e != nil {
			return ErrRefreshInvalid
		}
		if current.PreviousRefreshTokenHash == oldHash {
			reused = true
			revoke := tx.Rebind(`UPDATE identity_sessions SET revoked_at=?,revoke_reason='refresh_token_reuse',updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=?`)
			_, e := tx.ExecContext(systemCtx, revoke, time.Now(), time.Now(), current.UserID, current.ID, current.Version)
			return e
		}
		if current.RevokedAt != nil || !current.ExpiresAt.After(time.Now()) {
			return ErrRefreshInvalid
		}
		update := tx.Rebind(`UPDATE identity_sessions SET previous_refresh_token_hash=refresh_token_hash,refresh_token_hash=?,last_seen_at=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND revoked_at IS NULL`)
		result, e := tx.ExecContext(systemCtx, update, newHash, time.Now(), time.Now(), current.UserID, current.ID, current.Version)
		if e != nil {
			return e
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("rotate refresh token affected rows: %w", rowsErr)
		}
		if rows != 1 {
			return ErrRefreshInvalid
		}
		return nil
	})
	if err != nil {
		return Tokens{}, err
	}
	if reused {
		return Tokens{}, ErrRefreshReused
	}
	access, err := s.jwt.IssuePrincipal(platformprincipal.Principal{ID: current.UserID, Type: platformprincipal.TypeUser, SessionID: current.ID})
	if err != nil {
		return Tokens{}, err
	}
	return Tokens{AccessToken: access, RefreshToken: newRaw, TokenType: "Bearer", ExpiresIn: int64(s.cfg.JWT.TTL.Seconds()), SessionID: current.ID, UserID: current.UserID}, nil
}
func (s *Service) Logout(ctx context.Context, raw string) error {
	if raw == "" || len(raw) > 4096 {
		return ErrInvalid
	}
	systemCtx := platformprincipal.SystemContext(ctx, "identity-service:logout")
	return s.tx.Within(systemCtx, nil, func(tx *sqlx.Tx) error {
		q := tx.Rebind(`UPDATE identity_sessions SET revoked_at=?,revoke_reason='logout',updated_at=?,updated_by=?,version=version+1 WHERE refresh_token_hash=? AND revoked_at IS NULL AND deleted_at IS NULL`)
		result, err := tx.ExecContext(systemCtx, q, time.Now(), time.Now(), "identity-service:logout", tokenHash(raw))
		if err != nil {
			return err
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("logout affected rows: %w", rowsErr)
		}
		if rows != 1 {
			return ErrRefreshInvalid
		}
		return nil
	})
}
func newRefreshToken() (string, string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", "", fmt.Errorf("generate refresh token: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(value)
	return raw, tokenHash(raw), nil
}
func tokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
