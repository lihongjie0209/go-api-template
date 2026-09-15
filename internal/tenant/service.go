package tenant

import (
	"context"
	"encoding/json"
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

var validCode = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,62}$`)

type CreateInput struct{ Code, Name, Description, OwnerUsername string }
type UpdateInput struct {
	ID, Name, Description string
	Status                Status
	Version               int64
}
type PageInput struct {
	pagination.Request
	IDs           []string
	Statuses      []Status
	CreatedAtFrom *time.Time
	CreatedAtTo   *time.Time
}

type Service struct {
	repository *Repository
	transactor *database.Transactor
	cache      cache.Store
	operations operationlog.Recorder
	security   securitylog.Recorder
	users      UserResolver
	logger     *slog.Logger
	cacheTTL   time.Duration
}

func New(repository *Repository, transactor *database.Transactor, store cache.Store, operations operationlog.Recorder, security securitylog.Recorder, users UserResolver, logger *slog.Logger, cfg config.Config) *Service {
	return &Service{repository: repository, transactor: transactor, cache: store, operations: operations, security: security, users: users, logger: logger, cacheTTL: cfg.Tenant.CacheTTL}
}

func (s *Service) Create(ctx context.Context, input CreateInput) (View, error) {
	actor, err := requireActor(ctx)
	if err != nil {
		return View{}, err
	}
	input.Code = strings.ToLower(strings.TrimSpace(input.Code))
	input.Name = strings.TrimSpace(input.Name)
	input.OwnerUsername = strings.ToLower(strings.TrimSpace(input.OwnerUsername))
	if !validCode.MatchString(input.Code) || input.Name == "" || input.OwnerUsername == "" {
		return View{}, fmt.Errorf("%w: code, name and owner_username are required", ErrInvalid)
	}
	owner, err := s.users.ResolveUsername(ctx, input.OwnerUsername)
	if err != nil {
		return View{}, err
	}
	input.OwnerUsername = owner.Username
	record := Record{ID: uuid.NewString(), Code: input.Code, Name: input.Name, Description: input.Description, Status: StatusActive, OwnerUserID: owner.ID, OwnerName: owner.DisplayName}
	err = s.mutate(ctx, "tenant.create", record.ID, input, func(tx *sqlx.Tx) error {
		if err := insertTenant(ctx, tx, record, actor.ID); err != nil {
			return err
		}
		membershipID := uuid.NewString()
		if err := insertOwnerMembership(ctx, tx, membershipID, record, input.OwnerUsername, actor.ID); err != nil {
			return err
		}
		return insertTenantAdministrator(ctx, tx, uuid.NewString(), record.ID, membershipID, actor.ID)
	})
	if err != nil {
		return View{}, err
	}
	created, err := s.repository.AdminGet(ctx, record.ID)
	view := toView(created)
	return view, err
}

func (s *Service) Get(ctx context.Context, id string) (View, error) {
	actor, err := requireActor(ctx)
	if err != nil {
		return View{}, err
	}
	if id == "" {
		return View{}, fmt.Errorf("%w: id is required", ErrInvalid)
	}
	if actor.TenantID == "" {
		return View{}, ErrForbidden
	}
	key := "tenant:v1:" + actor.TenantID + ":" + id
	if s.cache != nil {
		if data, cacheErr := s.cache.Get(ctx, key); cacheErr == nil {
			var record Record
			if decodeErr := json.Unmarshal(data, &record); decodeErr == nil {
				if err := authorizeTenant(actor, record.ID); err != nil {
					return View{}, err
				}
				return toView(record), nil
			} else if s.logger != nil {
				s.logger.WarnContext(ctx, "decode tenant cache", "tenant_id", id, "error", decodeErr)
			}
		} else if !errors.Is(cacheErr, cache.ErrMiss) && s.logger != nil {
			s.logger.WarnContext(ctx, "read tenant cache", "tenant_id", id, "error", cacheErr)
		}
	}
	record, err := s.repository.Get(ctx, id, actor.TenantID)
	if err != nil {
		return View{}, err
	}
	if err := authorizeTenant(actor, record.ID); err != nil {
		return View{}, err
	}
	if s.cache != nil {
		if data, marshalErr := json.Marshal(record); marshalErr == nil {
			if cacheErr := s.cache.Set(ctx, key, data, s.cacheTTL); cacheErr != nil && s.logger != nil {
				s.logger.WarnContext(ctx, "write tenant cache", "tenant_id", id, "error", cacheErr)
			}
		}
	}
	return toView(record), nil
}

func (s *Service) Page(ctx context.Context, input PageInput) (pagination.Result[View], error) {
	actor, err := requireActor(ctx)
	if err != nil {
		return pagination.Result[View]{}, err
	}
	if actor.TenantID == "" {
		return pagination.Result[View]{}, ErrForbidden
	}
	request, err := pagination.Normalize(input.Request)
	if err != nil {
		return pagination.Result[View]{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if len(input.IDs) > 200 || len(input.Statuses) > 20 || (input.CreatedAtFrom != nil && input.CreatedAtTo != nil && !input.CreatedAtFrom.Before(*input.CreatedAtTo)) {
		return pagination.Result[View]{}, fmt.Errorf("%w: invalid tenant filters", ErrInvalid)
	}
	for _, status := range input.Statuses {
		if status != StatusActive && status != StatusDisabled {
			return pagination.Result[View]{}, fmt.Errorf("%w: invalid tenant status", ErrInvalid)
		}
	}
	input.Request = request
	input.Keyword = strings.TrimSpace(input.Keyword)
	records, total, err := s.repository.Page(ctx, actor.TenantID, input, request.PageSize, pagination.Offset(request))
	if err != nil {
		return pagination.Result[View]{}, err
	}
	items := make([]View, len(records))
	for i := range records {
		items[i] = toView(records[i])
	}
	return pagination.Result[View]{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (View, error) {
	actor, err := requireActor(ctx)
	if err != nil {
		return View{}, err
	}
	if err := authorizeTenant(actor, input.ID); err != nil {
		return View{}, err
	}
	if input.ID == "" || strings.TrimSpace(input.Name) == "" || input.Version <= 0 || (input.Status != StatusActive && input.Status != StatusDisabled) {
		return View{}, ErrInvalid
	}
	err = s.mutate(ctx, "tenant.update", input.ID, input, func(tx *sqlx.Tx) error {
		return updateTenant(ctx, tx, input.ID, strings.TrimSpace(input.Name), input.Description, input.Status, input.Version, actor.ID)
	})
	s.invalidate(ctx, input.ID)
	if err != nil {
		return View{}, err
	}
	view, err := s.Get(ctx, input.ID)
	return view, err
}

func (s *Service) Delete(ctx context.Context, id string, version int64) error {
	actor, err := requireActor(ctx)
	if err != nil {
		return err
	}
	if err := authorizeTenant(actor, id); err != nil {
		return err
	}
	if id == "" || version <= 0 {
		return ErrInvalid
	}
	err = s.mutate(ctx, "tenant.delete", id, map[string]any{"version": version}, func(tx *sqlx.Tx) error { return deleteTenant(ctx, tx, id, version, actor.ID) })
	s.invalidate(ctx, id)
	return err
}

var ErrInvalid = errors.New("invalid tenant input")
var ErrForbidden = errors.New("tenant access denied")

func requireActor(ctx context.Context) (platformprincipal.Principal, error) {
	actor, ok := platformprincipal.FromContext(ctx)
	if !ok {
		return actor, platformprincipal.ErrMissing
	}
	return actor, nil
}
func authorizeTenant(actor platformprincipal.Principal, tenantID string) error {
	if actor.TenantID == "" || actor.TenantID != tenantID {
		return ErrForbidden
	}
	return nil
}
func (s *Service) invalidate(ctx context.Context, id string) {
	if s.cache != nil {
		if err := s.cache.Delete(ctx, "tenant:v1:"+id+":"+id); err != nil && s.logger != nil {
			s.logger.WarnContext(ctx, "invalidate tenant cache", "tenant_id", id, "error", err)
		}
	}
}
func (s *Service) mutate(ctx context.Context, operation, id string, request any, fn func(*sqlx.Tx) error) error {
	committed := false
	err := operationlog.Do(ctx, s.operations, operationlog.Entry{Operation: operation, ResourceType: "tenant", ResourceID: id, Source: "backend", Protocol: "service", Request: request}, func() error {
		txErr := s.transactor.Within(ctx, nil, fn)
		committed = txErr == nil
		return txErr
	})
	securityErr := s.security.Record(ctx, securitylog.Entry{EventType: securitylog.EventTenantChanged, SubjectID: id, SubjectType: "tenant", TenantID: id, Succeeded: committed, Metadata: map[string]any{"operation": operation}})
	if securityErr != nil && committed && err == nil && s.security.FailClosed() {
		return securityErr
	}
	return err
}
