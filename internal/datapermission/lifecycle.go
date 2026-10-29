package datapermission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrPolicyNotFound               = errors.New("data permission: policy not found")
	ErrPolicyConflict               = errors.New("data permission: policy conflict")
	ErrVersionNotDraft              = errors.New("data permission: version is not draft")
	ErrPolicyScope                  = errors.New("data permission: policy scope denied")
	errActorResolutionFailed        = errors.New("data_permission_actor_resolution_failed")
	errCommittedPolicyRefreshFailed = errors.New("committed_policy_refresh_failed")
)

const (
	StatusActive     = "active"
	StatusDisabled   = "disabled"
	VersionDraft     = "draft"
	VersionPublished = "published"
	VersionArchived  = "archived"
)

type PolicyRecord struct {
	ID                     string          `db:"id" json:"id"`
	Code                   string          `db:"code" json:"code"`
	Name                   string          `db:"name" json:"name"`
	Scope                  PolicyScopeType `db:"scope" json:"scope"`
	TenantID               sql.NullString  `db:"tenant_id" json:"-"`
	PublishedVersionNumber sql.NullInt64   `db:"published_version_number" json:"published_version_number" swaggertype:"integer"`
	Status                 string          `db:"status" json:"status"`
	CreatedAt              time.Time       `db:"created_at" json:"created_at"`
	CreatedBy              string          `db:"created_by" json:"created_by"`
	CreatedByName          string          `db:"-" json:"created_by_name"`
	UpdatedAt              time.Time       `db:"updated_at" json:"updated_at"`
	UpdatedBy              string          `db:"updated_by" json:"updated_by"`
	UpdatedByName          string          `db:"-" json:"updated_by_name"`
	Version                int64           `db:"version" json:"version"`
}

func (record PolicyRecord) MarshalJSON() ([]byte, error) {
	type recordJSON PolicyRecord
	var publishedVersionNumber *int64
	if record.PublishedVersionNumber.Valid {
		publishedVersionNumber = &record.PublishedVersionNumber.Int64
	}
	return json.Marshal(struct {
		recordJSON
		PublishedVersionNumber *int64 `json:"published_version_number"`
	}{
		recordJSON:             recordJSON(record),
		PublishedVersionNumber: publishedVersionNumber,
	})
}

type VersionRecord struct {
	ID              string         `db:"id" json:"id"`
	PolicyID        string         `db:"policy_id" json:"policy_id"`
	VersionNumber   int64          `db:"version_number" json:"version_number"`
	Document        string         `db:"document" json:"document"`
	Status          string         `db:"status" json:"status"`
	PublishedAt     sql.NullTime   `db:"published_at" json:"published_at" swaggertype:"string" format:"date-time"`
	PublishedBy     sql.NullString `db:"published_by" json:"published_by" swaggertype:"string"`
	PublishedByName string         `db:"-" json:"published_by_name"`
	CreatedAt       time.Time      `db:"created_at" json:"created_at"`
	CreatedBy       string         `db:"created_by" json:"created_by"`
	CreatedByName   string         `db:"-" json:"created_by_name"`
	UpdatedAt       time.Time      `db:"updated_at" json:"updated_at"`
	UpdatedBy       string         `db:"updated_by" json:"updated_by"`
	UpdatedByName   string         `db:"-" json:"updated_by_name"`
	Version         int64          `db:"version" json:"version"`
}

func (record VersionRecord) MarshalJSON() ([]byte, error) {
	type recordJSON VersionRecord
	var publishedAt *time.Time
	if record.PublishedAt.Valid {
		publishedAt = &record.PublishedAt.Time
	}
	var publishedBy *string
	if record.PublishedBy.Valid {
		publishedBy = &record.PublishedBy.String
	}
	return json.Marshal(struct {
		recordJSON
		PublishedAt *time.Time `json:"published_at"`
		PublishedBy *string    `json:"published_by"`
	}{
		recordJSON:  recordJSON(record),
		PublishedAt: publishedAt,
		PublishedBy: publishedBy,
	})
}

type Publication struct {
	Policy  PolicyRecord  `json:"policy"`
	Version VersionRecord `json:"version"`
}

type PolicyPageInput struct {
	pagination.Request
	IDs, Codes                 []string
	Scopes                     []PolicyScopeType
	Statuses                   []string
	CreatedAtFrom, CreatedAtTo *time.Time
	UpdatedAtFrom, UpdatedAtTo *time.Time
}

type VersionPageInput struct {
	PolicyID string
	pagination.Request
	Statuses []string
}

type PolicyPage struct {
	Items    []PolicyRecord `json:"items"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Total    int64          `json:"total"`
}

type VersionPage struct {
	Items    []VersionRecord `json:"items"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
	Total    int64           `json:"total"`
}

type SetPolicyStatusInput struct {
	PolicyID              string `json:"policy_id"`
	Status                string `json:"status"`
	ExpectedPolicyVersion int64  `json:"expected_policy_version"`
}

type Repository struct{ db *sqlx.DB }

func NewRepository(db *sqlx.DB) *Repository { return &Repository{db: db} }

func (r *Repository) LoadAllPublished(ctx context.Context) ([]Policy, error) {
	return loadAllPublished(ctx, r.db)
}

func loadAllPublished(ctx context.Context, queryer sqlx.QueryerContext) ([]Policy, error) {
	rows := []struct {
		Code, Scope, Document string
		TenantID              sql.NullString `db:"tenant_id"`
	}{}
	query := `SELECT p.code,p.scope,p.tenant_id,v.document FROM data_permission_policies p
		JOIN data_permission_policy_versions v ON v.policy_id=p.id AND v.version_number=p.published_version_number
		 AND v.status='published' AND v.deleted_at IS NULL
		LEFT JOIN tenants t ON t.id=p.tenant_id AND t.status='active' AND t.deleted_at IS NULL
		WHERE p.status='active' AND p.deleted_at IS NULL
		 AND (p.scope='global' OR (p.scope='tenant' AND t.id IS NOT NULL))
		ORDER BY p.scope,COALESCE(p.tenant_id,''),p.code`
	if err := sqlx.SelectContext(ctx, queryer, &rows, query); err != nil {
		return nil, fmt.Errorf("load published data policies: %w", err)
	}
	policies := make([]Policy, 0, len(rows))
	for _, row := range rows {
		policy, err := ParsePolicy([]byte(row.Document))
		if err != nil {
			return nil, err
		}
		if policy.Metadata.Code != row.Code || string(policy.Scope.Type) != row.Scope || policy.Scope.TenantID != row.TenantID.String {
			return nil, ErrInvalidPolicy
		}
		policies = append(policies, policy)
	}
	return policies, nil
}

type RuntimeLoader struct {
	repository *Repository
	engine     *Engine
	sync       dataRuntimeSync
}

type dataRuntimeSync interface {
	Refresh(context.Context) error
	Changed(context.Context) error
	Run(context.Context)
	SetFailureHandler(func())
	Timeout() time.Duration
}

func NewRuntimeLoader(repository *Repository, engine *Engine) *RuntimeLoader {
	return &RuntimeLoader{repository: repository, engine: engine}
}
func (l *RuntimeLoader) ConfigureSync(sync dataRuntimeSync) {
	l.sync = sync
	if sync != nil && l.engine != nil {
		sync.SetFailureHandler(l.engine.Invalidate)
	}
}
func (l *RuntimeLoader) Initialize(ctx context.Context) error {
	if l.sync != nil {
		return l.sync.Refresh(ctx)
	}
	return l.Refresh(ctx)
}
func (l *RuntimeLoader) Changed(ctx context.Context) error {
	if l.sync != nil {
		return l.sync.Changed(ctx)
	}
	return l.Refresh(ctx)
}
func (l *RuntimeLoader) Run(ctx context.Context) {
	if l.sync != nil {
		l.sync.Run(ctx)
	}
}
func (l *RuntimeLoader) Revision() string {
	if l == nil {
		return ""
	}
	if source, ok := l.sync.(interface{ Revision() string }); ok {
		return source.Revision()
	}
	return ""
}
func (l *RuntimeLoader) Pin(ctx context.Context) (context.Context, string, error) {
	if l == nil || l.engine == nil {
		return ctx, "", ErrSnapshotUnavailable
	}
	return l.engine.Pin(ctx)
}
func (l *RuntimeLoader) Refresh(ctx context.Context) error {
	policies, err := l.repository.LoadAllPublished(ctx)
	if err != nil {
		if l.engine != nil {
			l.engine.Invalidate()
		}
		return err
	}
	if err := l.engine.Replace(policies); err != nil {
		l.engine.Invalidate()
		return err
	}
	return nil
}

// RefreshSnapshot installs policies and their matching database revision from
// one repeatable-read view, avoiding a stale snapshot with a newer revision.
func (l *RuntimeLoader) RefreshSnapshot(ctx context.Context) (string, error) {
	policies, revision, err := l.repository.LoadPublishedSnapshot(ctx)
	if err != nil {
		return "", err
	}
	if err := l.engine.Replace(policies); err != nil {
		return "", err
	}
	return revision, nil
}

type LifecycleService struct {
	repository *Repository
	transactor *database.Transactor
	schemas    *SchemaRegistry
	resources  *resourceaction.Registry
	runtime    *RuntimeLoader
	operations operationlog.TransactionalRecorder
	security   securitylog.TransactionalRecorder
	actors     presentation.ActorResolver
}

func NewLifecycleService(repository *Repository, transactor *database.Transactor, schemas *SchemaRegistry, resources *resourceaction.Registry, runtime *RuntimeLoader, operations operationlog.TransactionalRecorder, security securitylog.TransactionalRecorder, actors presentation.ActorResolver) *LifecycleService {
	return &LifecycleService{repository: repository, transactor: transactor, schemas: schemas, resources: resources, runtime: runtime, operations: operations, security: security, actors: actors}
}

func (s *LifecycleService) Get(ctx context.Context, id string) (PolicyRecord, error) {
	actor, err := lifecycleActor(ctx)
	if err != nil {
		return PolicyRecord{}, err
	}
	if id == "" {
		return PolicyRecord{}, ErrInvalidPolicy
	}
	record, err := s.repository.Get(ctx, id, actor.TenantID)
	if err != nil {
		return record, err
	}
	items := []PolicyRecord{record}
	s.presentPolicies(ctx, items)
	return items[0], nil
}

func (s *LifecycleService) GetVersion(ctx context.Context, policyID string, number int64) (VersionRecord, error) {
	actor, err := lifecycleActor(ctx)
	if err != nil {
		return VersionRecord{}, err
	}
	if policyID == "" || number <= 0 {
		return VersionRecord{}, ErrInvalidPolicy
	}
	record, err := s.repository.GetVersion(ctx, policyID, number, actor.TenantID)
	if err != nil {
		return record, err
	}
	items := []VersionRecord{record}
	s.presentVersions(ctx, items)
	return items[0], nil
}

func (s *LifecycleService) Page(ctx context.Context, input PolicyPageInput) (PolicyPage, error) {
	actor, err := lifecycleActor(ctx)
	if err != nil {
		return PolicyPage{}, err
	}
	page, err := s.repository.Page(ctx, actor.TenantID, input)
	if err == nil {
		s.presentPolicies(ctx, page.Items)
	}
	return page, err
}

func (s *LifecycleService) PageVersions(ctx context.Context, input VersionPageInput) (VersionPage, error) {
	actor, err := lifecycleActor(ctx)
	if err != nil {
		return VersionPage{}, err
	}
	page, err := s.repository.PageVersions(ctx, actor.TenantID, input)
	if err == nil {
		s.presentVersions(ctx, page.Items)
	}
	return page, err
}

func (s *LifecycleService) Create(ctx context.Context, policy Policy) (PolicyRecord, VersionRecord, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return PolicyRecord{}, VersionRecord{}, err
	}
	policy.normalize()
	if _, err := policy.Compile(s.schemas, s.resources); err != nil {
		return PolicyRecord{}, VersionRecord{}, err
	}
	if err := authorizeBoundary(actor, policy.Scope); err != nil {
		return PolicyRecord{}, VersionRecord{}, err
	}
	document, err := MarshalPolicy(policy)
	if err != nil {
		return PolicyRecord{}, VersionRecord{}, err
	}
	policyID, versionID, now := uuid.NewString(), uuid.NewString(), time.Now()
	var createdPolicy PolicyRecord
	var createdVersion VersionRecord
	err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		var tenant any
		if policy.Scope.Type == PolicyScopeTenant {
			var activeTenantID string
			err := tx.GetContext(ctx, &activeTenantID, tx.Rebind(`SELECT id FROM tenants WHERE id=? AND status='active' AND deleted_at IS NULL FOR UPDATE`), policy.Scope.TenantID)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: tenant is not active", ErrInvalidPolicy)
			}
			if err != nil {
				return fmt.Errorf("check policy tenant: %w", err)
			}
			tenant = policy.Scope.TenantID
		}
		q := tx.Rebind(`INSERT INTO data_permission_policies(id,code,name,scope,tenant_id,published_version_number,status,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,NULL,?,?,?,?,?,1)`)
		if _, e := tx.ExecContext(ctx, q, policyID, policy.Metadata.Code, policy.Metadata.Name, policy.Scope.Type, tenant, StatusActive, now, actor.ID, now, actor.ID); e != nil {
			if database.IsUniqueViolation(e) {
				return ErrPolicyConflict
			}
			return e
		}
		q = tx.Rebind(`INSERT INTO data_permission_policy_versions(id,policy_id,version_number,document,status,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,1)`)
		if _, e := tx.ExecContext(ctx, q, versionID, policyID, 1, string(document), VersionDraft, now, actor.ID, now, actor.ID); e != nil {
			return e
		}
		if err := insertActions(ctx, tx, versionID, policy, actor.ID, now); err != nil {
			return err
		}
		if err := s.recordChangeTx(ctx, tx, "data-permission.policy.create", policyID, policy, 1); err != nil {
			return err
		}
		createdPolicy, err = readPolicy(ctx, tx, tx.Rebind, policyID, actor.TenantID, false)
		if err != nil {
			return err
		}
		createdVersion, err = readVersion(ctx, tx, tx.Rebind, policyID, 1, actor.TenantID)
		return err
	})
	if err != nil {
		s.recordFailure(ctx, "data-permission.policy.create", policyID, policy)
		return PolicyRecord{}, VersionRecord{}, err
	}
	postCtx, cancel := s.postCommitContext(ctx)
	defer cancel()
	policies := []PolicyRecord{createdPolicy}
	versions := []VersionRecord{createdVersion}
	s.presentPolicies(postCtx, policies)
	s.presentVersions(postCtx, versions)
	return policies[0], versions[0], nil
}

func (s *LifecycleService) CreateVersion(ctx context.Context, policyID string, expectedVersion int64, policy Policy) (VersionRecord, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return VersionRecord{}, err
	}
	policy.normalize()
	if _, err := policy.Compile(s.schemas, s.resources); err != nil {
		return VersionRecord{}, err
	}
	if err := authorizeBoundary(actor, policy.Scope); err != nil {
		return VersionRecord{}, err
	}
	document, err := MarshalPolicy(policy)
	if err != nil {
		return VersionRecord{}, err
	}
	var number int64
	var createdVersion VersionRecord
	id, now := uuid.NewString(), time.Now()
	err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		current, e := lockPolicy(ctx, tx, policyID, actor.TenantID)
		if e != nil {
			return e
		}
		if current.Version != expectedVersion || current.Code != policy.Metadata.Code || current.Scope != policy.Scope.Type || current.TenantID.String != policy.Scope.TenantID {
			return ErrPolicyConflict
		}
		if e := tx.GetContext(ctx, &number, tx.Rebind(`SELECT COALESCE(max(version_number),0)+1 FROM data_permission_policy_versions WHERE policy_id=? AND deleted_at IS NULL`), policyID); e != nil {
			return e
		}
		q := tx.Rebind(`INSERT INTO data_permission_policy_versions(id,policy_id,version_number,document,status,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,?,1)`)
		if _, e := tx.ExecContext(ctx, q, id, policyID, number, string(document), VersionDraft, now, actor.ID, now, actor.ID); e != nil {
			return e
		}
		if e := insertActions(ctx, tx, id, policy, actor.ID, now); e != nil {
			return e
		}
		result, e := tx.ExecContext(ctx, tx.Rebind(`UPDATE data_permission_policies SET name=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`), policy.Metadata.Name, now, actor.ID, policyID, expectedVersion)
		if e != nil {
			return e
		}
		if e := requireRow(result); e != nil {
			return e
		}
		if err := s.recordChangeTx(ctx, tx, "data-permission.policy.version.create", policyID, policy, number); err != nil {
			return err
		}
		createdVersion, err = readVersion(ctx, tx, tx.Rebind, policyID, number, actor.TenantID)
		return err
	})
	if err != nil {
		s.recordFailure(ctx, "data-permission.policy.version.create", policyID, policy)
		return VersionRecord{}, err
	}
	postCtx, cancel := s.postCommitContext(ctx)
	defer cancel()
	versions := []VersionRecord{createdVersion}
	s.presentVersions(postCtx, versions)
	return versions[0], nil
}

func (s *LifecycleService) Publish(ctx context.Context, policyID string, number, expectedVersion int64) (PolicyRecord, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return PolicyRecord{}, err
	}
	now := time.Now()
	var policy Policy
	var updated PolicyRecord
	err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		current, e := lockPolicy(ctx, tx, policyID, actor.TenantID)
		if e != nil {
			return e
		}
		if current.Version != expectedVersion {
			return ErrPolicyConflict
		}
		var version VersionRecord
		if e := tx.GetContext(ctx, &version, tx.Rebind(`SELECT id,policy_id,version_number,document,status,version FROM data_permission_policy_versions WHERE policy_id=? AND version_number=? AND deleted_at IS NULL FOR UPDATE`), policyID, number); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return ErrPolicyNotFound
			}
			return fmt.Errorf("lock data permission policy version: %w", e)
		}
		if version.Status != VersionDraft {
			return ErrVersionNotDraft
		}
		policy, e = ParsePolicy([]byte(version.Document))
		if e != nil {
			return e
		}
		if _, e = policy.Compile(s.schemas, s.resources); e != nil {
			return e
		}
		if e = authorizeBoundary(actor, policy.Scope); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, tx.Rebind(`UPDATE data_permission_policy_versions SET status='archived',updated_at=?,updated_by=?,version=version+1 WHERE policy_id=? AND status='published' AND deleted_at IS NULL`), now, actor.ID, policyID); e != nil {
			return e
		}
		result, e := tx.ExecContext(ctx, tx.Rebind(`UPDATE data_permission_policy_versions SET status='published',published_at=?,published_by=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND status='draft' AND deleted_at IS NULL`), now, actor.ID, now, actor.ID, version.ID)
		if e != nil {
			return e
		}
		if e = requireRow(result); e != nil {
			return e
		}
		result, e = tx.ExecContext(ctx, tx.Rebind(`UPDATE data_permission_policies SET published_version_number=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`), number, now, actor.ID, policyID, expectedVersion)
		if e != nil {
			return e
		}
		if e = requireRow(result); e != nil {
			return e
		}
		if e = s.recordChangeTx(ctx, tx, "data-permission.policy.publish", policyID, policy, number); e != nil {
			return e
		}
		updated, e = lockPolicy(ctx, tx, policyID, actor.TenantID)
		return e
	})
	if err != nil {
		s.recordFailure(ctx, "data-permission.policy.publish", policyID, policy)
		return PolicyRecord{}, err
	}
	postCtx, cancel := s.postCommitContext(ctx)
	defer cancel()
	if s.runtime != nil {
		if err := s.runtime.Changed(postCtx); err != nil {
			slog.Default().ErrorContext(postCtx, "refresh committed data permission policy failed", "error", observability.SafeError(err, errCommittedPolicyRefreshFailed))
		}
	}
	records := []PolicyRecord{updated}
	s.presentPolicies(postCtx, records)
	return records[0], nil
}

// SetStatus activates or disables a policy without changing its immutable
// published version. The runtime snapshot is replaced after the transaction
// commits so new requests immediately observe the new state.
func (s *LifecycleService) SetStatus(ctx context.Context, input SetPolicyStatusInput) (PolicyRecord, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return PolicyRecord{}, err
	}
	if input.PolicyID == "" || input.ExpectedPolicyVersion <= 0 ||
		(input.Status != StatusActive && input.Status != StatusDisabled) {
		return PolicyRecord{}, ErrInvalidPolicy
	}
	now := time.Now()
	var policy Policy
	var updated PolicyRecord
	err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		current, lockErr := lockPolicy(ctx, tx, input.PolicyID, actor.TenantID)
		if lockErr != nil {
			return lockErr
		}
		if current.Version != input.ExpectedPolicyVersion {
			return ErrPolicyConflict
		}
		boundary := PolicyBoundary{Type: current.Scope, TenantID: current.TenantID.String}
		if boundary.Type == PolicyScopeGlobal {
			boundary.TenantID = ""
		}
		policy = Policy{Metadata: PolicyMetadata{Code: current.Code, Name: current.Name}, Scope: boundary}
		if boundaryErr := authorizeBoundary(actor, boundary); boundaryErr != nil {
			return boundaryErr
		}
		result, updateErr := tx.ExecContext(ctx, tx.Rebind(`UPDATE data_permission_policies SET status=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`), input.Status, now, actor.ID, input.PolicyID, input.ExpectedPolicyVersion)
		if updateErr != nil {
			return fmt.Errorf("update data permission policy status: %w", updateErr)
		}
		if rowErr := requireRow(result); rowErr != nil {
			return rowErr
		}
		if err := s.recordChangeTx(ctx, tx, "data-permission.policy.status.set", input.PolicyID, policy, 0); err != nil {
			return err
		}
		updated, err = lockPolicy(ctx, tx, input.PolicyID, actor.TenantID)
		return err
	})
	if err != nil {
		s.recordFailure(ctx, "data-permission.policy.status.set", input.PolicyID, policy)
		return PolicyRecord{}, err
	}
	postCtx, cancel := s.postCommitContext(ctx)
	defer cancel()
	if s.runtime != nil {
		if err := s.runtime.Changed(postCtx); err != nil {
			slog.Default().ErrorContext(postCtx, "refresh committed data permission policy failed", "error", observability.SafeError(err, errCommittedPolicyRefreshFailed))
		}
	}
	records := []PolicyRecord{updated}
	s.presentPolicies(postCtx, records)
	return records[0], nil
}

func (s *LifecycleService) postCommitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := 10 * time.Second
	if s.runtime != nil && s.runtime.sync != nil && s.runtime.sync.Timeout() > 0 {
		timeout = s.runtime.sync.Timeout() + 5*time.Second
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

func (s *LifecycleService) presentPolicies(ctx context.Context, records []PolicyRecord) {
	ids := make([]string, 0, len(records)*2)
	for _, record := range records {
		ids = append(ids, record.CreatedBy, record.UpdatedBy)
	}
	names := s.actorNames(ctx, ids)
	for index := range records {
		records[index].CreatedByName = names[records[index].CreatedBy]
		records[index].UpdatedByName = names[records[index].UpdatedBy]
		records[index].CreatedAt = presentation.Time(records[index].CreatedAt)
		records[index].UpdatedAt = presentation.Time(records[index].UpdatedAt)
	}
}

func (s *LifecycleService) presentVersions(ctx context.Context, records []VersionRecord) {
	ids := make([]string, 0, len(records)*3)
	for _, record := range records {
		ids = append(ids, record.CreatedBy, record.UpdatedBy)
		if record.PublishedBy.Valid {
			ids = append(ids, record.PublishedBy.String)
		}
	}
	names := s.actorNames(ctx, ids)
	for index := range records {
		records[index].CreatedByName = names[records[index].CreatedBy]
		records[index].UpdatedByName = names[records[index].UpdatedBy]
		if records[index].PublishedBy.Valid {
			records[index].PublishedByName = names[records[index].PublishedBy.String]
		}
		records[index].CreatedAt = presentation.Time(records[index].CreatedAt)
		records[index].UpdatedAt = presentation.Time(records[index].UpdatedAt)
		if records[index].PublishedAt.Valid {
			records[index].PublishedAt.Time = presentation.Time(records[index].PublishedAt.Time)
		}
	}
}

func (s *LifecycleService) actorNames(ctx context.Context, ids []string) map[string]string {
	names, err := presentation.ActorNames(ctx, s.actors, ids...)
	if err == nil {
		return names
	}
	slog.WarnContext(ctx, "resolve data permission actor names", "error", observability.SafeError(err, errActorResolutionFailed))
	names = make(map[string]string, len(ids))
	for _, id := range ids {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			names[id] = trimmed
		}
	}
	return names
}

func lockPolicy(ctx context.Context, tx *sqlx.Tx, id, tenantID string) (PolicyRecord, error) {
	return readPolicy(ctx, tx, tx.Rebind, id, tenantID, true)
}

func insertActions(ctx context.Context, tx *sqlx.Tx, versionID string, policy Policy, actor string, now time.Time) error {
	q := tx.Rebind(`INSERT INTO data_permission_policy_actions(id,policy_version_id,resource,action,created_at,created_by,updated_at,updated_by,version) VALUES(?,?,?,?,?,?,?,?,1)`)
	for _, action := range policy.Spec.Actions {
		if _, err := tx.ExecContext(ctx, q, uuid.NewString(), versionID, policy.Spec.Resource, action, now, actor, now, actor); err != nil {
			return err
		}
	}
	return nil
}

func authorizeBoundary(actor platformprincipal.Principal, boundary PolicyBoundary) error {
	if boundary.Type == PolicyScopeGlobal && actor.TenantID == "" {
		return nil
	}
	if boundary.Type == PolicyScopeTenant && actor.TenantID != "" && actor.TenantID == boundary.TenantID {
		return nil
	}
	return ErrPolicyScope
}

func lifecycleActor(ctx context.Context) (platformprincipal.Principal, error) {
	actor, ok := platformprincipal.FromContext(ctx)
	if !ok || actor.ID == "" {
		return platformprincipal.Principal{}, platformprincipal.ErrMissing
	}
	return actor, nil
}

func requireRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrPolicyConflict
	}
	return nil
}

func (s *LifecycleService) recordChangeTx(ctx context.Context, tx *sqlx.Tx, operation, policyID string, policy Policy, versionNumber int64) error {
	if s.operations != nil {
		entry := operationlog.Entry{
			Operation: operation, ResourceType: "data_permission_policy", ResourceID: policyID,
			ResourceName: policy.Metadata.Name, Source: "backend", Protocol: "service",
			Request: map[string]any{"policy_code": policy.Metadata.Code, "version_number": versionNumber}, Succeeded: true,
		}
		if err := s.operations.RecordTx(ctx, tx, entry); err != nil {
			return err
		}
	}
	if s.security != nil {
		entry := securitylog.Entry{
			EventType: securitylog.EventDataPolicyChanged, SubjectID: policyID,
			SubjectName: policy.Metadata.Name, SubjectType: "data_permission_policy", TenantID: policy.Scope.TenantID,
			Succeeded: true, Metadata: map[string]any{"operation": operation, "version_number": versionNumber},
		}
		if err := s.security.RecordTx(ctx, tx, entry); err != nil {
			return err
		}
	}
	return nil
}

func (s *LifecycleService) recordFailure(ctx context.Context, operation, policyID string, policy Policy) {
	if s.operations != nil {
		_ = s.operations.Record(ctx, operationlog.Entry{
			Operation: operation, ResourceType: "data_permission_policy", ResourceID: policyID,
			ResourceName: policy.Metadata.Name, Source: "backend", Protocol: "service",
			Succeeded: false, ErrorCode: "operation_failed", ErrorMessage: "operation failed",
		})
	}
	if s.security != nil {
		_ = s.security.Record(ctx, securitylog.Entry{
			EventType: securitylog.EventDataPolicyChanged, SubjectID: policyID,
			SubjectName: policy.Metadata.Name, SubjectType: "data_permission_policy", TenantID: policy.Scope.TenantID,
			Succeeded: false, ErrorCode: "operation_failed", ErrorMessage: "operation failed",
			Metadata: map[string]any{"operation": operation},
		})
	}
}
