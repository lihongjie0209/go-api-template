package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrInvalid  = errors.New("invalid user")
	ErrNotFound = errors.New("user not found")
	ErrConflict = errors.New("user conflict")
	ErrInUse    = errors.New("user is in use")
)

var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{2,63}$`)

const (
	maxUserIDLength      = 256
	maxDisplayNameLength = 256
	maxEmailLength       = 320
	maxPhoneLength       = 64
	maxUserKeywordLength = 256
)

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
	StatusLocked   Status = "locked"
	StatusClosed   Status = "closed"
)

type User struct {
	ID            string    `db:"id" json:"id"`
	Username      string    `db:"username" json:"username"`
	DisplayName   string    `db:"display_name" json:"display_name"`
	Email         string    `db:"email" json:"email"`
	Phone         string    `db:"phone" json:"phone"`
	Status        Status    `db:"status" json:"status"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
	CreatedBy     string    `db:"created_by" json:"created_by"`
	CreatedByName string    `db:"created_by_name" json:"created_by_name"`
	UpdatedAt     time.Time `db:"updated_at" json:"updated_at"`
	UpdatedBy     string    `db:"updated_by" json:"updated_by"`
	UpdatedByName string    `db:"updated_by_name" json:"updated_by_name"`
	Version       int64     `db:"version" json:"version"`
}

type CreateInput struct{ Username, DisplayName, Email, Phone string }
type UpdateInput struct {
	ID, DisplayName, Email, Phone string
	Status                        Status
	Version                       int64
}
type PageInput struct {
	pagination.Request
	IDs, Usernames, Emails, Phones []string
	Statuses                       []Status
	CreatedAtFrom                  *time.Time
	CreatedAtTo                    *time.Time
}

type Repository struct{ db *sqlx.DB }

func NewRepository(db *sqlx.DB) *Repository { return &Repository{db: db} }

const userColumns = `u.id,u.username,u.display_name,u.email,u.phone,u.status,u.created_at,u.created_by,
COALESCE((SELECT actor.display_name FROM identity_users actor WHERE actor.id=u.created_by),(SELECT actor.name FROM identity_service_accounts actor WHERE actor.id=u.created_by),u.created_by) AS created_by_name,
u.updated_at,u.updated_by,
COALESCE((SELECT actor.display_name FROM identity_users actor WHERE actor.id=u.updated_by),(SELECT actor.name FROM identity_service_accounts actor WHERE actor.id=u.updated_by),u.updated_by) AS updated_by_name,u.version`

func (r *Repository) Get(ctx context.Context, id string) (User, error) {
	return r.get(ctx, "id = ?", id)
}

func (r *Repository) ResolveUsername(ctx context.Context, username string) (User, error) {
	return r.get(ctx, "LOWER(username) = ?", normalizeUsername(username))
}

func (r *Repository) get(ctx context.Context, predicate string, args ...any) (User, error) {
	var user User
	query := r.db.Rebind(`SELECT ` + userColumns + ` FROM identity_users u WHERE ` + predicate + ` AND deleted_at IS NULL`)
	if err := r.db.GetContext(ctx, &user, query, args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("get identity user: %w", err)
	}
	return user, nil
}

func (r *Repository) Page(ctx context.Context, input PageInput) ([]User, int64, error) {
	where, args := "deleted_at IS NULL", []any{}
	if input.Keyword != "" {
		where += " AND (LOWER(username) LIKE ? OR LOWER(display_name) LIKE ? OR LOWER(email) LIKE ? OR phone LIKE ?)"
		pattern := "%" + strings.ToLower(input.Keyword) + "%"
		args = append(args, pattern, pattern, pattern, "%"+strings.TrimSpace(input.Keyword)+"%")
	}
	for _, filter := range []struct {
		clause string
		value  any
		count  int
	}{
		{"id IN (?)", input.IDs, len(input.IDs)}, {"LOWER(username) IN (?)", normalizeUsernames(input.Usernames), len(input.Usernames)}, {"LOWER(email) IN (?)", normalizeUsernames(input.Emails), len(input.Emails)}, {"phone IN (?)", trimValues(input.Phones), len(input.Phones)}, {"status IN (?)", input.Statuses, len(input.Statuses)},
	} {
		if filter.count > 0 {
			where += " AND " + filter.clause
			args = append(args, filter.value)
		}
	}
	if input.CreatedAtFrom != nil {
		where += " AND created_at >= ?"
		args = append(args, *input.CreatedAtFrom)
	}
	if input.CreatedAtTo != nil {
		where += " AND created_at < ?"
		args = append(args, *input.CreatedAtTo)
	}
	where, args, err := sqlx.In(where, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("build user filters: %w", err)
	}
	var total int64
	if err := r.db.GetContext(ctx, &total, r.db.Rebind(`SELECT count(*) FROM identity_users WHERE `+where), args...); err != nil {
		return nil, 0, err
	}
	queryArgs := append(append([]any{}, args...), input.PageSize, pagination.Offset(input.Request))
	users := []User{}
	err = r.db.SelectContext(ctx, &users, r.db.Rebind(`SELECT `+userColumns+` FROM identity_users u WHERE `+where+` ORDER BY created_at DESC,id LIMIT ? OFFSET ?`), queryArgs...)
	return users, total, err
}

type Service struct {
	repository *Repository
	transactor *database.Transactor
	cache      cache.Store
	operations operationlog.TransactionalRecorder
	security   securitylog.TransactionalRecorder
	logger     *slog.Logger
	cacheTTL   time.Duration
}

func New(repository *Repository, transactor *database.Transactor, store cache.Store, operations operationlog.TransactionalRecorder, security securitylog.TransactionalRecorder, logger *slog.Logger, cfg config.Config) *Service {
	return &Service{repository: repository, transactor: transactor, cache: store, operations: operations, security: security, logger: logger, cacheTTL: cfg.User.CacheTTL}
}
func (s *Service) Get(ctx context.Context, id string) (User, error) {
	if _, err := actor(ctx); err != nil {
		return User{}, err
	}
	if strings.TrimSpace(id) == "" || len(id) > maxUserIDLength {
		return User{}, ErrInvalid
	}
	return s.cached(ctx, "id:"+id, func() (User, error) { return s.repository.Get(ctx, id) })
}
func (s *Service) ResolveUsername(ctx context.Context, username string) (User, error) {
	username = normalizeUsername(username)
	if !usernamePattern.MatchString(username) {
		return User{}, ErrInvalid
	}
	return s.cached(ctx, "username:"+username, func() (User, error) { return s.repository.ResolveUsername(ctx, username) })
}

// ResolveUsernameAuthoritative bypasses the disposable cache for security
// decisions such as login eligibility. A stale cached status must never allow
// a disabled or deleted user to authenticate.
func (s *Service) ResolveUsernameAuthoritative(ctx context.Context, username string) (User, error) {
	username = normalizeUsername(username)
	if !usernamePattern.MatchString(username) {
		return User{}, ErrInvalid
	}
	return s.repository.ResolveUsername(ctx, username)
}
func (s *Service) Create(ctx context.Context, input CreateInput) (User, error) {
	a, e := actor(ctx)
	input.Username = normalizeUsername(input.Username)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.Phone = strings.TrimSpace(input.Phone)
	if e != nil {
		return User{}, e
	}
	if !usernamePattern.MatchString(input.Username) || !validUserProfile(input.DisplayName, input.Email, input.Phone) {
		return User{}, ErrInvalid
	}
	id := uuid.NewString()
	now := time.Now()
	e = s.mutate(ctx, "identity.user.create", id, input, func(tx *sqlx.Tx) error {
		q := tx.Rebind(`INSERT INTO identity_users (id,username,display_name,email,phone,status,created_at,created_by,updated_at,updated_by,version) VALUES (?,?,?,?,?,?,?,?,?,?,1)`)
		_, err := tx.ExecContext(ctx, q, id, input.Username, input.DisplayName, input.Email, input.Phone, StatusActive, now, a, now, a)
		if uniqueViolation(err) {
			return ErrConflict
		}
		return err
	})
	if e != nil {
		return User{}, fmt.Errorf("create user: %w", e)
	}
	user, e := s.repository.Get(ctx, id)
	if e == nil {
		s.cacheUser(ctx, user)
	}
	return user, e
}
func (s *Service) Page(ctx context.Context, input PageInput) (pagination.Result[User], error) {
	if _, e := actor(ctx); e != nil {
		return pagination.Result[User]{}, e
	}
	request, e := pagination.Normalize(input.Request)
	if e != nil || len(input.Keyword) > maxUserKeywordLength || len(input.IDs) > 200 || len(input.Usernames) > 200 || len(input.Emails) > 200 || len(input.Phones) > 200 || len(input.Statuses) > 20 || !validBoundedValues(input.IDs, maxUserIDLength, false) || !validBoundedValues(input.Usernames, 64, false) || !validBoundedValues(input.Emails, maxEmailLength, true) || !validBoundedValues(input.Phones, maxPhoneLength, true) || (input.CreatedAtFrom != nil && input.CreatedAtTo != nil && !input.CreatedAtFrom.Before(*input.CreatedAtTo)) {
		return pagination.Result[User]{}, ErrInvalid
	}
	input.Keyword = strings.TrimSpace(input.Keyword)
	for _, username := range input.Usernames {
		if !usernamePattern.MatchString(normalizeUsername(username)) {
			return pagination.Result[User]{}, ErrInvalid
		}
	}
	for _, status := range input.Statuses {
		if !validStatus(status) {
			return pagination.Result[User]{}, ErrInvalid
		}
	}
	input.Request = request
	users, total, e := s.repository.Page(ctx, input)
	return pagination.Result[User]{Items: users, Page: request.Page, PageSize: request.PageSize, Total: total}, e
}
func (s *Service) Update(ctx context.Context, input UpdateInput) (User, error) {
	a, e := actor(ctx)
	if e != nil {
		return User{}, e
	}
	if input.ID == "" || len(input.ID) > maxUserIDLength || input.Version <= 0 || !validUserProfile(input.DisplayName, input.Email, input.Phone) || !validStatus(input.Status) {
		return User{}, ErrInvalid
	}
	input.DisplayName, input.Email, input.Phone = strings.TrimSpace(input.DisplayName), strings.ToLower(strings.TrimSpace(input.Email)), strings.TrimSpace(input.Phone)
	existing, e := s.repository.Get(ctx, input.ID)
	if e != nil {
		return User{}, e
	}
	e = s.mutate(ctx, "identity.user.update", input.ID, input, func(tx *sqlx.Tx) error {
		if input.Status != StatusActive {
			now := time.Now()
			if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE identity_sessions SET revoked_at=?,revoke_reason='user_status_changed',updated_at=?,updated_by=?,version=version+1 WHERE user_id=? AND revoked_at IS NULL AND deleted_at IS NULL`), now, now, a, input.ID); err != nil {
				return err
			}
		}
		q := tx.Rebind(`UPDATE identity_users SET display_name=?,email=?,phone=?,status=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`)
		result, err := tx.ExecContext(ctx, q, input.DisplayName, input.Email, input.Phone, input.Status, time.Now(), a, input.ID, input.Version)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("user update affected rows: %w", err)
		}
		if rows != 1 {
			return ErrConflict
		}
		return nil
	})
	if e != nil {
		return User{}, e
	}
	s.invalidate(ctx, existing)
	user, e := s.repository.Get(ctx, input.ID)
	if e == nil {
		s.cacheUser(ctx, user)
	}
	return user, e
}
func (s *Service) Delete(ctx context.Context, id string, version int64) error {
	a, e := actor(ctx)
	if e != nil {
		return e
	}
	if id == "" || len(id) > maxUserIDLength || version <= 0 {
		return ErrInvalid
	}
	existing, e := s.repository.Get(ctx, id)
	if e != nil {
		return e
	}
	e = s.mutate(ctx, "identity.user.delete", id, map[string]any{"name": identityUserDisplayName(existing), "version": version}, func(tx *sqlx.Tx) error {
		for _, query := range []string{
			`SELECT count(*) FROM tenants WHERE owner_user_id=? AND deleted_at IS NULL`,
			`SELECT count(*) FROM tenant_memberships WHERE user_id=? AND deleted_at IS NULL`,
		} {
			var references int
			if err := tx.GetContext(ctx, &references, tx.Rebind(query), id); err != nil {
				return err
			}
			if references > 0 {
				return ErrInUse
			}
		}
		now := time.Now()
		if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE identity_sessions SET revoked_at=?,revoke_reason='user_deleted',updated_at=?,updated_by=?,version=version+1 WHERE user_id=? AND revoked_at IS NULL AND deleted_at IS NULL`), now, now, a, id); err != nil {
			return err
		}
		q := tx.Rebind(`UPDATE identity_users SET status=?,deleted_at=?,deleted_by=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`)
		result, err := tx.ExecContext(ctx, q, StatusClosed, now, a, now, a, id, version)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("user delete affected rows: %w", err)
		}
		if rows != 1 {
			return ErrConflict
		}
		return nil
	})
	if e == nil {
		s.invalidate(ctx, existing)
	}
	return e
}

func (s *Service) cached(ctx context.Context, key string, loader func() (User, error)) (User, error) {
	if s.cache != nil {
		value, err := cache.GetJSON[User](ctx, s.cache, "identity:user:v1:"+key)
		if err == nil {
			return value, nil
		}
		if !errors.Is(err, cache.ErrMiss) {
			s.logger.WarnContext(ctx, "read identity user cache", "error", err)
		}
	}
	value, err := loader()
	if err != nil {
		return User{}, err
	}
	s.cacheUser(ctx, value)
	return value, nil
}
func (s *Service) cacheUser(ctx context.Context, user User) {
	if s.cache == nil {
		return
	}
	for _, key := range []string{"identity:user:v1:id:" + user.ID, "identity:user:v1:username:" + normalizeUsername(user.Username)} {
		if err := cache.SetJSON(ctx, s.cache, key, user, s.cacheTTL); err != nil {
			s.logger.WarnContext(ctx, "write identity user cache", "user_id", user.ID, "error", err)
		}
	}
}
func (s *Service) invalidate(ctx context.Context, user User) {
	if s.cache == nil {
		return
	}
	cacheCtx, cancel := cache.AfterCommitContext(ctx)
	defer cancel()
	if err := s.cache.Delete(cacheCtx, "identity:user:v1:id:"+user.ID, "identity:user:v1:username:"+normalizeUsername(user.Username)); err != nil {
		s.logger.WarnContext(cacheCtx, "invalidate identity user cache", "user_id", user.ID, "error", err)
	}
}
func (s *Service) mutate(ctx context.Context, operation, id string, request any, fn func(*sqlx.Tx) error) error {
	started := time.Now()
	resourceName := identityMutationName(request, id)
	operationEntry := operationlog.Entry{Operation: operation, ResourceType: "identity_user", ResourceID: id, ResourceName: resourceName, Source: "backend", Protocol: "service", Request: request}
	securityEntry := securitylog.Entry{EventType: securitylog.EventIdentityUserChanged, SubjectID: id, SubjectName: resourceName, SubjectType: "identity_user", Metadata: map[string]any{"operation": operation}}
	err := s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		operationEntry.Duration = time.Since(started)
		operationEntry.Succeeded = true
		if err := s.operations.RecordTx(ctx, tx, operationEntry); err != nil {
			return err
		}
		securityEntry.Succeeded = true
		return s.security.RecordTx(ctx, tx, securityEntry)
	})
	if err != nil {
		operationEntry.Duration = time.Since(started)
		operationEntry.Succeeded = false
		operationEntry.ErrorCode = "operation_failed"
		operationEntry.ErrorMessage = "operation failed"
		_ = s.operations.Record(ctx, operationEntry)
		securityEntry.Succeeded = false
		securityEntry.ErrorCode = "operation_failed"
		securityEntry.ErrorMessage = "operation failed"
		_ = s.security.Record(ctx, securityEntry)
	}
	return err
}

func identityUserDisplayName(user User) string {
	if name := strings.TrimSpace(user.DisplayName); name != "" {
		return name
	}
	return strings.TrimSpace(user.Username)
}

func identityMutationName(request any, fallback string) string {
	switch value := request.(type) {
	case CreateInput:
		if value.DisplayName != "" {
			return value.DisplayName
		}
		return value.Username
	case UpdateInput:
		return value.DisplayName
	case map[string]any:
		if name, ok := value["name"].(string); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	return fallback
}
func actor(ctx context.Context) (string, error) {
	principal, ok := platformprincipal.FromContext(ctx)
	if !ok {
		return "", platformprincipal.ErrMissing
	}
	return principal.ID, nil
}
func normalizeUsername(v string) string { return strings.ToLower(strings.TrimSpace(v)) }
func normalizeUsernames(v []string) []string {
	out := make([]string, len(v))
	for i := range v {
		out[i] = normalizeUsername(v[i])
	}
	return out
}
func trimValues(v []string) []string {
	out := make([]string, len(v))
	for i := range v {
		out[i] = strings.TrimSpace(v[i])
	}
	return out
}
func validStatus(v Status) bool {
	return v == StatusActive || v == StatusDisabled || v == StatusLocked || v == StatusClosed
}
func validUserProfile(displayName, email, phone string) bool {
	return strings.TrimSpace(displayName) != "" && len(displayName) <= maxDisplayNameLength && len(email) <= maxEmailLength && len(phone) <= maxPhoneLength
}
func validBoundedValues(values []string, maximum int, allowEmpty bool) bool {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if len(value) > maximum || (!allowEmpty && value == "") {
			return false
		}
	}
	return true
}
func uniqueViolation(err error) bool {
	return database.IsUniqueViolation(err)
}
