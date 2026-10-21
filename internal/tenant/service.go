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
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var validCode = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,62}$`)

const (
	maxTenantIDLength          = 128
	maxTenantNameLength        = 256
	maxTenantDescriptionLength = 4096
	maxTenantKeywordLength     = 256
)

type CreateInput struct{ Code, Name, Description, OwnerUsername string }
type UpdateInput struct {
	ID, Name, Description string
	Status                Status
	Version               int64
}
type PageInput struct {
	pagination.Request
	IDs           []string
	Codes         []string
	OwnerUserIDs  []string
	Statuses      []Status
	CreatedAtFrom *time.Time
	CreatedAtTo   *time.Time
}

type Service struct {
	repository    *Repository
	transactor    *database.Transactor
	cache         cache.Store
	policies      PolicyRuntime
	operations    operationlog.TransactionalRecorder
	security      securitylog.TransactionalRecorder
	users         UserResolver
	logger        *slog.Logger
	cacheTTL      time.Duration
	policyTimeout time.Duration
}

// PolicyRuntime refreshes the published data-permission snapshot after a
// tenant update or deletion commits. Its database revision poll recovers lost hints.
type PolicyRuntime interface {
	Changed(context.Context) error
}

func New(repository *Repository, transactor *database.Transactor, store cache.Store, operations operationlog.TransactionalRecorder, security securitylog.TransactionalRecorder, users UserResolver, logger *slog.Logger, cfg config.Config) *Service {
	return &Service{repository: repository, transactor: transactor, cache: store, operations: operations, security: security, users: users, logger: logger, cacheTTL: cfg.Tenant.CacheTTL, policyTimeout: cfg.PolicySync.Timeout}
}

// SetPolicyRuntime is called during dependency injection before listeners run.
func (s *Service) SetPolicyRuntime(runtime PolicyRuntime) { s.policies = runtime }

func (s *Service) Create(ctx context.Context, input CreateInput) (View, error) {
	actor, err := requirePlatformActor(ctx)
	if err != nil {
		return View{}, err
	}
	input.Code = strings.ToLower(strings.TrimSpace(input.Code))
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.OwnerUsername = strings.ToLower(strings.TrimSpace(input.OwnerUsername))
	if !validCode.MatchString(input.Code) || input.Name == "" || len(input.Name) > maxTenantNameLength || len(input.Description) > maxTenantDescriptionLength || input.OwnerUsername == "" || len(input.OwnerUsername) > 256 {
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
	if err != nil {
		return View{}, err
	}
	views, err := s.present(ctx, []Record{created})
	if err != nil {
		return View{}, err
	}
	return views[0], nil
}

// AdminGet reads a tenant outside a tenant context. It is intentionally
// separate from Get so an empty tenant ID can never widen a tenant-scoped read.
func (s *Service) AdminGet(ctx context.Context, id string) (View, error) {
	if _, err := requirePlatformActor(ctx); err != nil {
		return View{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxTenantIDLength {
		return View{}, fmt.Errorf("%w: id is required", ErrInvalid)
	}
	record, err := s.repository.AdminGet(ctx, id)
	if err != nil {
		return View{}, err
	}
	views, err := s.present(ctx, []Record{record})
	if err != nil {
		return View{}, err
	}
	return views[0], nil
}

// AdminDeleteTarget returns a fresh narrow projection for a platform operator
// that may hold delete permission without tenant read or list permissions.
func (s *Service) AdminDeleteTarget(ctx context.Context, id string) (DeleteTarget, error) {
	if _, err := requirePlatformActor(ctx); err != nil {
		return DeleteTarget{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxTenantIDLength {
		return DeleteTarget{}, ErrInvalid
	}
	return s.repository.AdminDeleteTarget(ctx, id)
}

// AdminUpdateTarget returns fresh editable fields to platform operators with
// tenant update permission but no tenant read or list permission.
func (s *Service) AdminUpdateTarget(ctx context.Context, id string) (UpdateTarget, error) {
	if _, err := requirePlatformActor(ctx); err != nil {
		return UpdateTarget{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxTenantIDLength {
		return UpdateTarget{}, ErrInvalid
	}
	return s.repository.AdminUpdateTarget(ctx, id)
}

// AdminPage is the only service operation allowed to list tenants without a
// tenant predicate. Operation authorization must additionally grant its platform
// permission before the handler is entered.
func (s *Service) AdminPage(ctx context.Context, input PageInput) (pagination.Result[View], error) {
	if _, err := requirePlatformActor(ctx); err != nil {
		return pagination.Result[View]{}, err
	}
	request, err := normalizePageInput(&input)
	if err != nil {
		return pagination.Result[View]{}, err
	}
	records, total, err := s.repository.AdminPage(ctx, input, request.PageSize, pagination.Offset(request))
	if err != nil {
		return pagination.Result[View]{}, err
	}
	items, err := s.present(ctx, records)
	if err != nil {
		return pagination.Result[View]{}, err
	}
	return pagination.Result[View]{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func (s *Service) Get(ctx context.Context, id string) (View, error) {
	actor, err := requireActor(ctx)
	if err != nil {
		return View{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxTenantIDLength {
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
				views, presentErr := s.present(ctx, []Record{record})
				if presentErr != nil {
					return View{}, presentErr
				}
				return views[0], nil
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
	views, err := s.present(ctx, []Record{record})
	if err != nil {
		return View{}, err
	}
	return views[0], nil
}

// DeleteTarget reads the current tenant directly from the database so its
// optimistic version cannot be served from the normal tenant cache.
func (s *Service) DeleteTarget(ctx context.Context) (DeleteTarget, error) {
	actor, err := requireActor(ctx)
	if err != nil {
		return DeleteTarget{}, err
	}
	if strings.TrimSpace(actor.TenantID) == "" {
		return DeleteTarget{}, ErrForbidden
	}
	return s.repository.DeleteTarget(ctx, actor.TenantID)
}

// UpdateTarget derives the tenant from the principal and bypasses cached reads
// so an update-only caller receives the current optimistic version.
func (s *Service) UpdateTarget(ctx context.Context) (UpdateTarget, error) {
	actor, err := requireActor(ctx)
	if err != nil {
		return UpdateTarget{}, err
	}
	if strings.TrimSpace(actor.TenantID) == "" {
		return UpdateTarget{}, ErrForbidden
	}
	return s.repository.UpdateTarget(ctx, actor.TenantID)
}

func (s *Service) Page(ctx context.Context, input PageInput) (pagination.Result[View], error) {
	actor, err := requireActor(ctx)
	if err != nil {
		return pagination.Result[View]{}, err
	}
	if actor.TenantID == "" {
		return pagination.Result[View]{}, ErrForbidden
	}
	request, err := normalizePageInput(&input)
	if err != nil {
		return pagination.Result[View]{}, err
	}
	records, total, err := s.repository.Page(ctx, actor.TenantID, input, request.PageSize, pagination.Offset(request))
	if err != nil {
		return pagination.Result[View]{}, err
	}
	items, err := s.present(ctx, records)
	if err != nil {
		return pagination.Result[View]{}, err
	}
	return pagination.Result[View]{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}

func (s *Service) present(ctx context.Context, records []Record) ([]View, error) {
	ids := make([]string, 0, len(records)*2)
	for _, record := range records {
		ids = append(ids, record.CreatedBy, record.UpdatedBy)
	}
	names := stableActorNames(ids)
	if resolver, ok := s.users.(presentation.ActorResolver); ok {
		resolved, err := resolver.ResolveUserIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		for id, name := range resolved {
			names[id] = name
		}
	}
	views := make([]View, len(records))
	for index := range records {
		records[index].CreatedByName = names[records[index].CreatedBy]
		records[index].UpdatedByName = names[records[index].UpdatedBy]
		views[index] = toView(records[index])
	}
	return views, nil
}

func (s *Service) AdminUpdate(ctx context.Context, input UpdateInput) (View, error) {
	actor, err := requirePlatformActor(ctx)
	if err != nil {
		return View{}, err
	}
	input.ID = strings.TrimSpace(input.ID)
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.ID == "" || len(input.ID) > maxTenantIDLength || input.Name == "" || len(input.Name) > maxTenantNameLength || len(input.Description) > maxTenantDescriptionLength || input.Version <= 0 || (input.Status != StatusActive && input.Status != StatusDisabled) {
		return View{}, ErrInvalid
	}
	err = s.mutate(ctx, "platform.tenant.update", input.ID, input, func(tx *sqlx.Tx) error {
		return updateTenant(ctx, tx, input.ID, input.Name, input.Description, input.Status, input.Version, actor.ID)
	})
	if err != nil {
		return View{}, err
	}
	if err := s.afterTenantMutation(ctx, input.ID); err != nil {
		return View{}, err
	}
	return s.AdminGet(ctx, input.ID)
}

func (s *Service) AdminDelete(ctx context.Context, id string, version int64) error {
	actor, err := requirePlatformActor(ctx)
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxTenantIDLength || version <= 0 {
		return ErrInvalid
	}
	current, err := s.repository.AdminGet(ctx, id)
	if err != nil {
		return err
	}
	err = s.mutate(ctx, "platform.tenant.delete", id, map[string]any{"name": current.Name, "version": version}, func(tx *sqlx.Tx) error {
		return deleteTenant(ctx, tx, id, version, actor.ID)
	})
	if err == nil {
		return s.afterTenantMutation(ctx, id)
	}
	return err
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (View, error) {
	actor, err := requireActor(ctx)
	if err != nil {
		return View{}, err
	}
	if err := authorizeTenant(actor, input.ID); err != nil {
		return View{}, err
	}
	input.ID = strings.TrimSpace(input.ID)
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.ID == "" || len(input.ID) > maxTenantIDLength || input.Name == "" || len(input.Name) > maxTenantNameLength || len(input.Description) > maxTenantDescriptionLength || input.Version <= 0 || (input.Status != StatusActive && input.Status != StatusDisabled) {
		return View{}, ErrInvalid
	}
	err = s.mutate(ctx, "tenant.update", input.ID, input, func(tx *sqlx.Tx) error {
		return updateTenant(ctx, tx, input.ID, input.Name, input.Description, input.Status, input.Version, actor.ID)
	})
	if err != nil {
		return View{}, err
	}
	if err := s.afterTenantMutation(ctx, input.ID); err != nil {
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
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxTenantIDLength || version <= 0 {
		return ErrInvalid
	}
	current, err := s.repository.Get(ctx, id, actor.TenantID)
	if err != nil {
		return err
	}
	err = s.mutate(ctx, "tenant.delete", id, map[string]any{"name": current.Name, "version": version}, func(tx *sqlx.Tx) error { return deleteTenant(ctx, tx, id, version, actor.ID) })
	if err == nil {
		return s.afterTenantMutation(ctx, id)
	}
	return err
}

func (s *Service) afterTenantMutation(ctx context.Context, id string) error {
	s.invalidate(ctx, id)
	if s.policies != nil {
		timeout := s.policyTimeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout+time.Second)
		defer cancel()
		if err := s.policies.Changed(refreshCtx); err != nil {
			return fmt.Errorf("refresh tenant data permission policies: %w", err)
		}
	}
	return nil
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

func requirePlatformActor(ctx context.Context) (platformprincipal.Principal, error) {
	actor, err := requireActor(ctx)
	if err != nil {
		return actor, err
	}
	if actor.TenantID != "" {
		return actor, ErrForbidden
	}
	return actor, nil
}

func normalizePageInput(input *PageInput) (pagination.Request, error) {
	request, err := pagination.Normalize(input.Request)
	if err != nil {
		return pagination.Request{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	input.Keyword = strings.TrimSpace(input.Keyword)
	if len(input.Keyword) > maxTenantKeywordLength || len(input.IDs) > 200 || len(input.Codes) > 200 || len(input.OwnerUserIDs) > 200 || len(input.Statuses) > 20 || !boundedStrings(input.IDs, maxTenantIDLength) || !boundedStrings(input.OwnerUserIDs, maxTenantIDLength) || (input.CreatedAtFrom != nil && input.CreatedAtTo != nil && !input.CreatedAtFrom.Before(*input.CreatedAtTo)) {
		return pagination.Request{}, fmt.Errorf("%w: invalid tenant filters", ErrInvalid)
	}
	for index, code := range input.Codes {
		code = strings.ToLower(strings.TrimSpace(code))
		if !validCode.MatchString(code) {
			return pagination.Request{}, fmt.Errorf("%w: invalid tenant code", ErrInvalid)
		}
		input.Codes[index] = code
	}
	for index, id := range input.OwnerUserIDs {
		input.OwnerUserIDs[index] = strings.TrimSpace(id)
	}
	for _, status := range input.Statuses {
		if status != StatusActive && status != StatusDisabled {
			return pagination.Request{}, fmt.Errorf("%w: invalid tenant status", ErrInvalid)
		}
	}
	input.Request = request
	return request, nil
}

func boundedStrings(values []string, maxLength int) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > maxLength {
			return false
		}
	}
	return true
}
func authorizeTenant(actor platformprincipal.Principal, tenantID string) error {
	if actor.TenantID == "" || actor.TenantID != tenantID {
		return ErrForbidden
	}
	return nil
}
func (s *Service) invalidate(ctx context.Context, id string) {
	if s.cache != nil {
		cacheCtx, cancel := cache.AfterCommitContext(ctx)
		defer cancel()
		if err := s.cache.Delete(cacheCtx, "tenant:v1:"+id+":"+id); err != nil && s.logger != nil {
			s.logger.WarnContext(cacheCtx, "invalidate tenant cache", "tenant_id", id, "error", err)
		}
	}
}
func (s *Service) mutate(ctx context.Context, operation, id string, request any, fn func(*sqlx.Tx) error) error {
	started := time.Now()
	resourceName := tenantMutationName(request, id)
	operationEntry := operationlog.Entry{Operation: operation, ResourceType: "tenant", ResourceID: id, ResourceName: resourceName, Source: "backend", Protocol: "service", Request: request}
	securityEntry := securitylog.Entry{EventType: securitylog.EventTenantChanged, SubjectID: id, SubjectName: resourceName, SubjectType: "tenant", TenantID: id, Metadata: map[string]any{"operation": operation}}
	err := s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		operationEntry.Duration = time.Since(started)
		operationEntry.Succeeded = true
		if s.operations != nil {
			if err := s.operations.RecordTx(ctx, tx, operationEntry); err != nil {
				return err
			}
		}
		securityEntry.Succeeded = true
		if s.security != nil {
			return s.security.RecordTx(ctx, tx, securityEntry)
		}
		return nil
	})
	if err != nil {
		operationEntry.Duration = time.Since(started)
		operationEntry.Succeeded = false
		operationEntry.ErrorCode = "operation_failed"
		operationEntry.ErrorMessage = "operation failed"
		if s.operations != nil {
			_ = s.operations.Record(ctx, operationEntry)
		}
		securityEntry.Succeeded = false
		securityEntry.ErrorCode = "operation_failed"
		securityEntry.ErrorMessage = "operation failed"
		if s.security != nil {
			_ = s.security.Record(ctx, securityEntry)
		}
	}
	return err
}

func tenantMutationName(request any, fallback string) string {
	switch value := request.(type) {
	case CreateInput:
		return value.Name
	case UpdateInput:
		return value.Name
	case map[string]any:
		if name, ok := value["name"].(string); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	return fallback
}
