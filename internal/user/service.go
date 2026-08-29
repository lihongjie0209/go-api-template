package user

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/cache"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
	cacheKeyPrefix  = "user:"
)

type Service struct {
	repository  Repository
	transactor  *database.Transactor
	redis       *redis.Client
	locker      *cache.Locker
	cfg         config.User
	logger      *slog.Logger
	idempotency *idempotency.Manager
}

func NewService(repository Repository, transactor *database.Transactor, client *redis.Client, locker *cache.Locker, manager *idempotency.Manager, cfg config.Config, logger *slog.Logger) *Service {
	return &Service{repository: repository, transactor: transactor, redis: client, locker: locker, idempotency: manager, cfg: cfg.User, logger: logger}
}

func (s *Service) Create(ctx context.Context, name, email string) (User, error) {
	name, email, err := validate(name, email)
	if err != nil {
		return User{}, err
	}
	key, hasKey := idempotency.FromContext(ctx)
	if !hasKey {
		return s.createOnce(ctx, name, email)
	}
	if s.idempotency == nil || !s.idempotency.Enabled() {
		return User{}, apperror.Unavailable("idempotency is unavailable", nil)
	}
	fingerprintBytes := sha256.Sum256([]byte("user.create\x00" + name + "\x00" + email))
	decision, err := s.idempotency.Begin(ctx, key, hex.EncodeToString(fingerprintBytes[:]))
	if err != nil {
		return User{}, apperror.Unavailable("idempotency is unavailable", err)
	}
	switch decision.State {
	case idempotency.StateCompleted:
		var replay User
		if err := json.Unmarshal(decision.Response, &replay); err != nil {
			return User{}, apperror.Internal(err)
		}
		return replay, nil
	case idempotency.StateFailed:
		return User{}, apperror.New(decision.Failure.Code, decision.Failure.Message, decision.Failure.HTTPStatus, nil)
	case idempotency.StateProcessing:
		return User{}, apperror.RequestInProgress()
	case idempotency.StateConflict:
		return User{}, apperror.Conflict("idempotency key was used with a different request", nil)
	case idempotency.StateAcquired:
	default:
		return User{}, apperror.Internal(errors.New("unknown idempotency state"))
	}
	created, createErr := s.createOnce(ctx, name, email)
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if createErr != nil {
		var appErr *apperror.Error
		if !errors.As(createErr, &appErr) {
			appErr = apperror.Internal(createErr)
		}
		if err := s.idempotency.Fail(finishCtx, key, decision.Owner, idempotency.Failure{Code: appErr.Code, Message: appErr.Message, HTTPStatus: appErr.HTTPStatus}); err != nil {
			s.logger.WarnContext(finishCtx, "store idempotency failure failed", "error", err)
		}
		return User{}, createErr
	}
	if err := s.idempotency.Complete(finishCtx, key, decision.Owner, created); err != nil {
		s.logger.WarnContext(finishCtx, "store idempotency result failed", "error", err)
	}
	return created, nil
}

func (s *Service) createOnce(ctx context.Context, name, email string) (User, error) {
	if !s.transactor.Available() {
		return User{}, apperror.Unavailable("database is unavailable", nil)
	}
	if s.locker != nil {
		lock, lockErr := s.locker.Lock(ctx, emailLockKey(email), s.cfg.LockTTL, s.cfg.LockRetryDelay)
		if lockErr != nil {
			return User{}, apperror.Unavailable("user creation is temporarily unavailable", lockErr)
		}
		defer func() {
			unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			if unlockErr := lock.Unlock(unlockCtx); unlockErr != nil {
				s.logger.WarnContext(unlockCtx, "release user creation lock failed", "error", unlockErr)
			}
		}()
	}
	now := time.Now().UTC()
	created := User{ID: uuid.NewString(), Name: name, Email: email, Version: 1, CreatedAt: now, UpdatedAt: now}
	if txErr := s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		return s.repository.Create(ctx, tx, created)
	}); txErr != nil {
		return User{}, translate(txErr)
	}
	s.cacheSet(ctx, created)
	return created, nil
}

func (s *Service) Get(ctx context.Context, id string) (User, error) {
	if _, err := uuid.Parse(id); err != nil {
		return User{}, apperror.Invalid("invalid user id", err)
	}
	if !s.transactor.Available() {
		return User{}, apperror.Unavailable("database is unavailable", nil)
	}
	if cached, ok := s.cacheGet(ctx, id); ok {
		return cached, nil
	}
	user, err := s.repository.Get(ctx, id)
	if err != nil {
		return User{}, translate(err)
	}
	s.cacheSet(ctx, user)
	return user, nil
}

func (s *Service) List(ctx context.Context, page, pageSize int) (Page, error) {
	if !s.transactor.Available() {
		return Page{}, apperror.Unavailable("database is unavailable", nil)
	}
	if page == 0 {
		page = 1
	}
	if pageSize == 0 {
		pageSize = defaultPageSize
	}
	if page < 1 || pageSize < 1 || pageSize > maxPageSize {
		return Page{}, apperror.Invalid("page must be positive and page_size must be between 1 and 100", nil)
	}
	users, total, err := s.repository.List(ctx, pageSize, (page-1)*pageSize)
	if err != nil {
		return Page{}, translate(err)
	}
	return Page{Users: users, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *Service) Update(ctx context.Context, id, name, email string, version int64) (User, error) {
	if _, err := uuid.Parse(id); err != nil {
		return User{}, apperror.Invalid("invalid user id", err)
	}
	if !s.transactor.Available() {
		return User{}, apperror.Unavailable("database is unavailable", nil)
	}
	name, email, err := validate(name, email)
	if err != nil {
		return User{}, err
	}
	if version < 1 {
		return User{}, apperror.Invalid("version must be positive", nil)
	}
	if err := s.repository.Update(ctx, User{ID: id, Name: name, Email: email, Version: version, UpdatedAt: time.Now().UTC()}); err != nil {
		return User{}, translate(err)
	}
	s.cacheDelete(ctx, id)
	return s.Get(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id string, version int64) error {
	if _, err := uuid.Parse(id); err != nil {
		return apperror.Invalid("invalid user id", err)
	}
	if !s.transactor.Available() {
		return apperror.Unavailable("database is unavailable", nil)
	}
	if version < 1 {
		return apperror.Invalid("version must be positive", nil)
	}
	if err := s.repository.Delete(ctx, id, version); err != nil {
		return translate(err)
	}
	s.cacheDelete(ctx, id)
	return nil
}

func validate(name, email string) (string, string, error) {
	name = strings.TrimSpace(name)
	email = strings.ToLower(strings.TrimSpace(email))
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 100 {
		return "", "", apperror.Invalid("name must contain between 1 and 100 characters", nil)
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 {
		return "", "", apperror.Invalid("invalid email", err)
	}
	return name, email, nil
}

func translate(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return apperror.NotFound("user not found")
	case errors.Is(err, ErrConflict):
		return apperror.Conflict("email already exists", err)
	case errors.Is(err, ErrVersion):
		return apperror.Conflict("user was changed or does not exist", err)
	default:
		return apperror.Internal(err)
	}
}

func emailLockKey(email string) string {
	sum := sha256.Sum256([]byte(email))
	return "user:create:" + hex.EncodeToString(sum[:])
}

func (s *Service) cacheGet(ctx context.Context, id string) (User, bool) {
	if s.redis == nil {
		return User{}, false
	}
	data, err := s.redis.Get(ctx, cacheKeyPrefix+id).Bytes()
	if errors.Is(err, redis.Nil) {
		return User{}, false
	}
	if err != nil {
		s.logger.WarnContext(ctx, "read user cache failed", "error", err)
		return User{}, false
	}
	var user User
	if err := json.Unmarshal(data, &user); err != nil {
		s.logger.WarnContext(ctx, "decode user cache failed", "error", err)
		return User{}, false
	}
	return user, true
}

func (s *Service) cacheSet(ctx context.Context, user User) {
	if s.redis == nil {
		return
	}
	data, err := json.Marshal(user)
	if err != nil {
		s.logger.WarnContext(ctx, "encode user cache failed", "error", err)
		return
	}
	if err := s.redis.Set(ctx, cacheKeyPrefix+user.ID, data, s.cfg.CacheTTL).Err(); err != nil {
		s.logger.WarnContext(ctx, "write user cache failed", "error", err)
	}
}

func (s *Service) cacheDelete(ctx context.Context, id string) {
	if s.redis != nil {
		if err := s.redis.Del(ctx, cacheKeyPrefix+id).Err(); err != nil {
			s.logger.WarnContext(ctx, "delete user cache failed", "error", err)
		}
	}
}

var Module = fx.Module("user", fx.Provide(database.NewTransactor, NewRepository, NewService))
