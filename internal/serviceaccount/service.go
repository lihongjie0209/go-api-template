package serviceaccount

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrInvalid            = errors.New("invalid service account")
	ErrNotFound           = errors.New("service account not found")
	ErrConflict           = errors.New("service account conflict")
	ErrInvalidCredentials = errors.New("invalid service account credentials")
)

var clientIDPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{2,127}$`)

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

type Account struct {
	ID             string     `db:"id" json:"id"`
	ClientID       string     `db:"client_id" json:"client_id"`
	Name           string     `db:"name" json:"name"`
	Description    string     `db:"description" json:"description"`
	Status         Status     `db:"status" json:"status"`
	ExpiresAt      *time.Time `db:"expires_at" json:"expires_at,omitempty"`
	LastUsedAt     *time.Time `db:"last_used_at" json:"last_used_at,omitempty"`
	FailedAttempts int64      `db:"failed_attempts" json:"failed_attempts"`
	LockedUntil    *time.Time `db:"locked_until" json:"locked_until,omitempty"`
	CreatedAt      time.Time  `db:"created_at" json:"created_at"`
	CreatedBy      string     `db:"created_by" json:"created_by"`
	UpdatedAt      time.Time  `db:"updated_at" json:"updated_at"`
	UpdatedBy      string     `db:"updated_by" json:"updated_by"`
	Version        int64      `db:"version" json:"version"`
}

type Created struct {
	Account Account `json:"account"`
	Secret  string  `json:"secret"`
}

type CreateInput struct {
	ClientID, Name, Description string
	ExpiresAt                   *time.Time
}

type UpdateInput struct {
	ID, Name, Description string
	Status                Status
	ExpiresAt             *time.Time
	Version               int64
}

type PageInput struct {
	pagination.Request
	IDs, ClientIDs []string
	Statuses       []Status
	CreatedAtFrom  *time.Time
	CreatedAtTo    *time.Time
	ExpiresAtFrom  *time.Time
	ExpiresAtTo    *time.Time
}

type credential struct {
	Account
	SecretHash string `db:"secret_hash"`
}

type Service struct {
	db                *sqlx.DB
	transactor        *database.Transactor
	hasher            *auth.PasswordHasher
	operations        operationlog.Recorder
	security          securitylog.Recorder
	maxFailedAttempts int64
	lockDuration      time.Duration
}

const accountColumns = `id,client_id,name,description,status,expires_at,last_used_at,failed_attempts,locked_until,created_at,created_by,updated_at,updated_by,version`

func New(db *sqlx.DB, transactor *database.Transactor, operations operationlog.Recorder, security securitylog.Recorder, cfg config.Config) *Service {
	return &Service{db: db, transactor: transactor, hasher: auth.NewPasswordHasher(), operations: operations, security: security, maxFailedAttempts: cfg.Authentication.MaxFailedAttempts, lockDuration: cfg.Authentication.LockDuration}
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Created, error) {
	actor, err := requireActor(ctx)
	input.ClientID = normalizeClientID(input.ClientID)
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if err != nil {
		return Created{}, err
	}
	if !clientIDPattern.MatchString(input.ClientID) || input.Name == "" || len(input.Name) > 256 || len(input.Description) > 4096 || expired(input.ExpiresAt, time.Now()) {
		return Created{}, ErrInvalid
	}
	secret, err := newSecret()
	if err != nil {
		return Created{}, fmt.Errorf("generate service account secret: %w", err)
	}
	hash, err := s.hasher.Hash(secret)
	if err != nil {
		return Created{}, fmt.Errorf("hash service account secret: %w", err)
	}
	id, now := uuid.NewString(), time.Now()
	err = s.mutate(ctx, "identity.service-account.create", id, safeRequest(input), func(tx *sqlx.Tx) error {
		query := tx.Rebind(`INSERT INTO identity_service_accounts (id,client_id,name,description,secret_hash,status,expires_at,failed_attempts,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,1)`)
		_, execErr := tx.ExecContext(ctx, query, id, input.ClientID, input.Name, input.Description, hash, StatusActive, input.ExpiresAt, 0, now, actor.ID, now, actor.ID)
		if database.IsUniqueViolation(execErr) {
			return ErrConflict
		}
		return execErr
	})
	if err != nil {
		return Created{}, err
	}
	account, err := s.get(ctx, "id=?", id)
	return Created{Account: account, Secret: secret}, err
}

func (s *Service) Get(ctx context.Context, id string) (Account, error) {
	if _, err := requireActor(ctx); err != nil {
		return Account{}, err
	}
	if strings.TrimSpace(id) == "" {
		return Account{}, ErrInvalid
	}
	return s.get(ctx, "id=?", id)
}

func (s *Service) Page(ctx context.Context, input PageInput) (pagination.Result[Account], error) {
	if _, err := requireActor(ctx); err != nil {
		return pagination.Result[Account]{}, err
	}
	request, err := pagination.Normalize(input.Request)
	if err != nil || len(input.IDs) > 200 || len(input.ClientIDs) > 200 || len(input.Statuses) > 20 || invalidRange(input.CreatedAtFrom, input.CreatedAtTo) || invalidRange(input.ExpiresAtFrom, input.ExpiresAtTo) {
		return pagination.Result[Account]{}, ErrInvalid
	}
	for _, status := range input.Statuses {
		if !validStatus(status) {
			return pagination.Result[Account]{}, ErrInvalid
		}
	}
	where, args := "deleted_at IS NULL", []any{}
	if keyword := strings.ToLower(strings.TrimSpace(input.Keyword)); keyword != "" {
		where += " AND (LOWER(client_id) LIKE ? OR LOWER(name) LIKE ? OR LOWER(description) LIKE ?)"
		pattern := "%" + keyword + "%"
		args = append(args, pattern, pattern, pattern)
	}
	for _, filter := range []struct {
		clause string
		value  any
		count  int
	}{
		{"id IN (?)", input.IDs, len(input.IDs)},
		{"LOWER(client_id) IN (?)", normalizeClientIDs(input.ClientIDs), len(input.ClientIDs)},
		{"status IN (?)", input.Statuses, len(input.Statuses)},
	} {
		if filter.count > 0 {
			where += " AND " + filter.clause
			args = append(args, filter.value)
		}
	}
	for _, bound := range []struct {
		clause string
		value  *time.Time
	}{
		{"created_at>=?", input.CreatedAtFrom}, {"created_at<?", input.CreatedAtTo},
		{"expires_at>=?", input.ExpiresAtFrom}, {"expires_at<?", input.ExpiresAtTo},
	} {
		if bound.value != nil {
			where += " AND " + bound.clause
			args = append(args, *bound.value)
		}
	}
	where, args, err = sqlx.In(where, args...)
	if err != nil {
		return pagination.Result[Account]{}, fmt.Errorf("build service account filters: %w", err)
	}
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*) FROM identity_service_accounts WHERE `+where), args...); err != nil {
		return pagination.Result[Account]{}, err
	}
	queryArgs := append(append([]any{}, args...), request.PageSize, pagination.Offset(request))
	items := []Account{}
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(`SELECT `+accountColumns+` FROM identity_service_accounts WHERE `+where+` ORDER BY created_at DESC,id LIMIT ? OFFSET ?`), queryArgs...); err != nil {
		return pagination.Result[Account]{}, err
	}
	return pagination.Result[Account]{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (Account, error) {
	actor, err := requireActor(ctx)
	input.ID, input.Name, input.Description = strings.TrimSpace(input.ID), strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
	if err != nil {
		return Account{}, err
	}
	if input.ID == "" || input.Version <= 0 || input.Name == "" || len(input.Name) > 256 || len(input.Description) > 4096 || !validStatus(input.Status) || expired(input.ExpiresAt, time.Now()) {
		return Account{}, ErrInvalid
	}
	err = s.mutate(ctx, "identity.service-account.update", input.ID, safeRequest(input), func(tx *sqlx.Tx) error {
		query := tx.Rebind(`UPDATE identity_service_accounts SET name=?,description=?,status=?,expires_at=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`)
		result, execErr := tx.ExecContext(ctx, query, input.Name, input.Description, input.Status, input.ExpiresAt, time.Now(), actor.ID, input.ID, input.Version)
		return oneRow(result, execErr)
	})
	if err != nil {
		return Account{}, err
	}
	return s.get(ctx, "id=?", input.ID)
}

func (s *Service) RotateSecret(ctx context.Context, id string, version int64) (Created, error) {
	actor, err := requireActor(ctx)
	if err != nil {
		return Created{}, err
	}
	if strings.TrimSpace(id) == "" || version <= 0 {
		return Created{}, ErrInvalid
	}
	secret, err := newSecret()
	if err != nil {
		return Created{}, err
	}
	hash, err := s.hasher.Hash(secret)
	if err != nil {
		return Created{}, err
	}
	err = s.mutate(ctx, "identity.service-account.secret.rotate", id, map[string]any{"version": version}, func(tx *sqlx.Tx) error {
		query := tx.Rebind(`UPDATE identity_service_accounts SET secret_hash=?,last_used_at=NULL,failed_attempts=0,locked_until=NULL,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`)
		result, execErr := tx.ExecContext(ctx, query, hash, time.Now(), actor.ID, id, version)
		return oneRow(result, execErr)
	})
	if err != nil {
		return Created{}, err
	}
	account, err := s.get(ctx, "id=?", id)
	return Created{Account: account, Secret: secret}, err
}

func (s *Service) Delete(ctx context.Context, id string, version int64) error {
	actor, err := requireActor(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" || version <= 0 {
		return ErrInvalid
	}
	return s.mutate(ctx, "identity.service-account.delete", id, map[string]any{"version": version}, func(tx *sqlx.Tx) error {
		now := time.Now()
		query := tx.Rebind(`UPDATE identity_service_accounts SET status=?,deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`)
		result, execErr := tx.ExecContext(ctx, query, StatusDisabled, now, actor.ID, now, actor.ID, id, version)
		return oneRow(result, execErr)
	})
}

func (s *Service) Authenticate(ctx context.Context, clientID, secret string) (Account, error) {
	clientID = normalizeClientID(clientID)
	if !clientIDPattern.MatchString(clientID) || secret == "" || len(secret) > 1024 {
		return Account{}, ErrInvalidCredentials
	}
	var value credential
	query := s.db.Rebind(`SELECT ` + accountColumns + `,secret_hash FROM identity_service_accounts WHERE client_id=? AND status=? AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at>?)`)
	if err := s.db.GetContext(ctx, &value, query, clientID, StatusActive, time.Now()); err != nil {
		return Account{}, ErrInvalidCredentials
	}
	if value.LockedUntil != nil && value.LockedUntil.After(time.Now()) {
		return Account{}, ErrInvalidCredentials
	}
	valid, err := s.hasher.Verify(secret, value.SecretHash)
	if err != nil || !valid {
		s.recordFailure(ctx, value.Account)
		return Account{}, ErrInvalidCredentials
	}
	// Authentication success must not fail because this diagnostic timestamp races
	// or the database becomes unavailable after credential verification. Still use
	// an actor-bound transaction so the shared audit trigger is never bypassed.
	accountCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: value.ID, Type: platformprincipal.TypeServiceAccount})
	_ = s.transactor.Within(accountCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		_, updateErr := tx.ExecContext(accountCtx, tx.Rebind(`UPDATE identity_service_accounts SET last_used_at=?,failed_attempts=0,locked_until=NULL,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND deleted_at IS NULL`), now, now, value.ID, value.ID)
		return updateErr
	})
	return value.Account, nil
}

func (s *Service) recordFailure(ctx context.Context, account Account) {
	if s.maxFailedAttempts <= 0 || s.lockDuration <= 0 {
		return
	}
	accountCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: account.ID, Type: platformprincipal.TypeServiceAccount})
	_ = s.transactor.Within(accountCtx, nil, func(tx *sqlx.Tx) error {
		now := time.Now()
		lockedUntil := now.Add(s.lockDuration)
		query := tx.Rebind(`UPDATE identity_service_accounts SET failed_attempts=failed_attempts+1,locked_until=CASE WHEN failed_attempts+1>=? THEN ? ELSE locked_until END,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND deleted_at IS NULL`)
		_, err := tx.ExecContext(accountCtx, query, s.maxFailedAttempts, lockedUntil, now, account.ID, account.ID)
		return err
	})
}

func (s *Service) get(ctx context.Context, predicate string, args ...any) (Account, error) {
	var account Account
	query := s.db.Rebind(`SELECT ` + accountColumns + ` FROM identity_service_accounts WHERE ` + predicate + ` AND deleted_at IS NULL`)
	if err := s.db.GetContext(ctx, &account, query, args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Account{}, ErrNotFound
		}
		return Account{}, fmt.Errorf("get service account: %w", err)
	}
	return account, nil
}

func (s *Service) mutate(ctx context.Context, operation, id string, request any, fn func(*sqlx.Tx) error) error {
	committed := false
	err := operationlog.Do(ctx, s.operations, operationlog.Entry{Operation: operation, ResourceType: "service_account", ResourceID: id, Source: "backend", Protocol: "service", Request: request}, func() error {
		txErr := s.transactor.Within(ctx, nil, fn)
		committed = txErr == nil
		return txErr
	})
	logErr := s.security.Record(ctx, securitylog.Entry{EventType: securitylog.EventServiceAccountChanged, SubjectID: id, SubjectType: "service_account", Succeeded: committed, Metadata: map[string]any{"operation": operation}})
	if logErr != nil && err == nil && s.security.FailClosed() {
		return logErr
	}
	return err
}

func requireActor(ctx context.Context) (platformprincipal.Principal, error) {
	return platformprincipal.Require(ctx)
}

func newSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func normalizeClientID(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func normalizeClientIDs(values []string) []string {
	result := make([]string, len(values))
	for index := range values {
		result[index] = normalizeClientID(values[index])
	}
	return result
}
func validStatus(value Status) bool { return value == StatusActive || value == StatusDisabled }
func expired(value *time.Time, now time.Time) bool {
	return value != nil && !value.After(now)
}
func invalidRange(from, to *time.Time) bool {
	return from != nil && to != nil && !from.Before(*to)
}
func safeRequest(value any) any {
	switch input := value.(type) {
	case CreateInput:
		return map[string]any{"client_id": input.ClientID, "name": input.Name, "description": input.Description, "expires_at": input.ExpiresAt}
	case UpdateInput:
		return map[string]any{"id": input.ID, "name": input.Name, "description": input.Description, "status": input.Status, "expires_at": input.ExpiresAt, "version": input.Version}
	default:
		return value
	}
}
func oneRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("service account affected rows: %w", err)
	}
	if rows != 1 {
		return ErrConflict
	}
	return nil
}
