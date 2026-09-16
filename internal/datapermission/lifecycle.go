package datapermission

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

var (
	ErrPolicyNotFound  = errors.New("data permission: policy not found")
	ErrPolicyConflict  = errors.New("data permission: policy conflict")
	ErrVersionNotDraft = errors.New("data permission: version is not draft")
	ErrPolicyScope     = errors.New("data permission: policy scope denied")
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
	PublishedVersionNumber sql.NullInt64   `db:"published_version_number" json:"-"`
	Status                 string          `db:"status" json:"status"`
	CreatedAt              time.Time       `db:"created_at" json:"created_at"`
	CreatedBy              string          `db:"created_by" json:"created_by"`
	UpdatedAt              time.Time       `db:"updated_at" json:"updated_at"`
	UpdatedBy              string          `db:"updated_by" json:"updated_by"`
	Version                int64           `db:"version" json:"version"`
}

type VersionRecord struct {
	ID            string         `db:"id" json:"id"`
	PolicyID      string         `db:"policy_id" json:"policy_id"`
	VersionNumber int64          `db:"version_number" json:"version_number"`
	Document      string         `db:"document" json:"document"`
	Status        string         `db:"status" json:"status"`
	PublishedAt   sql.NullTime   `db:"published_at" json:"-"`
	PublishedBy   sql.NullString `db:"published_by" json:"-"`
	CreatedAt     time.Time      `db:"created_at" json:"created_at"`
	CreatedBy     string         `db:"created_by" json:"created_by"`
	UpdatedAt     time.Time      `db:"updated_at" json:"updated_at"`
	UpdatedBy     string         `db:"updated_by" json:"updated_by"`
	Version       int64          `db:"version" json:"version"`
}

type Publication struct {
	Policy  PolicyRecord  `json:"policy"`
	Version VersionRecord `json:"version"`
}

type PolicyPageInput struct {
	pagination.Request
	Scopes   []PolicyScopeType
	Statuses []string
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
	rows := []struct {
		Code, Scope, Document string
		TenantID              sql.NullString `db:"tenant_id"`
	}{}
	query := `SELECT p.code,p.scope,p.tenant_id,v.document FROM data_permission_policies p
		JOIN data_permission_policy_versions v ON v.policy_id=p.id AND v.version_number=p.published_version_number
		 AND v.status='published' AND v.deleted_at IS NULL
		WHERE p.status='active' AND p.deleted_at IS NULL ORDER BY p.scope,COALESCE(p.tenant_id,''),p.code`
	if err := r.db.SelectContext(ctx, &rows, query); err != nil {
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
}

func NewRuntimeLoader(repository *Repository, engine *Engine) *RuntimeLoader {
	return &RuntimeLoader{repository: repository, engine: engine}
}
func (l *RuntimeLoader) ConfigureSync(sync dataRuntimeSync) { l.sync = sync }
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
func (l *RuntimeLoader) Refresh(ctx context.Context) error {
	policies, err := l.repository.LoadAllPublished(ctx)
	if err != nil {
		return err
	}
	return l.engine.Replace(policies)
}

type LifecycleService struct {
	repository *Repository
	transactor *database.Transactor
	schemas    *SchemaRegistry
	resources  *pbac.Registry
	runtime    *RuntimeLoader
	operations operationlog.TransactionalRecorder
	security   securitylog.TransactionalRecorder
}

func NewLifecycleService(repository *Repository, transactor *database.Transactor, schemas *SchemaRegistry, resources *pbac.Registry, runtime *RuntimeLoader, operations operationlog.TransactionalRecorder, security securitylog.TransactionalRecorder) *LifecycleService {
	return &LifecycleService{repository: repository, transactor: transactor, schemas: schemas, resources: resources, runtime: runtime, operations: operations, security: security}
}

func (s *LifecycleService) Get(ctx context.Context, id string) (PolicyRecord, error) {
	actor, err := lifecycleActor(ctx)
	if err != nil {
		return PolicyRecord{}, err
	}
	if id == "" {
		return PolicyRecord{}, ErrInvalidPolicy
	}
	return s.repository.Get(ctx, id, actor.TenantID)
}

func (s *LifecycleService) GetVersion(ctx context.Context, policyID string, number int64) (VersionRecord, error) {
	actor, err := lifecycleActor(ctx)
	if err != nil {
		return VersionRecord{}, err
	}
	if policyID == "" || number <= 0 {
		return VersionRecord{}, ErrInvalidPolicy
	}
	return s.repository.GetVersion(ctx, policyID, number, actor.TenantID)
}

func (s *LifecycleService) Page(ctx context.Context, input PolicyPageInput) (PolicyPage, error) {
	actor, err := lifecycleActor(ctx)
	if err != nil {
		return PolicyPage{}, err
	}
	return s.repository.Page(ctx, actor.TenantID, input)
}

func (s *LifecycleService) PageVersions(ctx context.Context, input VersionPageInput) (VersionPage, error) {
	actor, err := lifecycleActor(ctx)
	if err != nil {
		return VersionPage{}, err
	}
	return s.repository.PageVersions(ctx, actor.TenantID, input)
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
	err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		var tenant any
		if policy.Scope.Type == PolicyScopeTenant {
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
		return s.recordChangeTx(ctx, tx, "data-permission.policy.create", policyID, policy, 1)
	})
	if err != nil {
		s.recordFailure(ctx, "data-permission.policy.create", policyID, policy)
		return PolicyRecord{}, VersionRecord{}, err
	}
	return s.get(ctx, policyID, 1, actor.TenantID)
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
		return s.recordChangeTx(ctx, tx, "data-permission.policy.version.create", policyID, policy, number)
	})
	if err != nil {
		s.recordFailure(ctx, "data-permission.policy.version.create", policyID, policy)
		return VersionRecord{}, err
	}
	return s.repository.GetVersion(ctx, policyID, number, actor.TenantID)
}

func (s *LifecycleService) Publish(ctx context.Context, policyID string, number, expectedVersion int64) (PolicyRecord, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil {
		return PolicyRecord{}, err
	}
	now := time.Now()
	var policy Policy
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
			return ErrPolicyNotFound
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
		return s.recordChangeTx(ctx, tx, "data-permission.policy.publish", policyID, policy, number)
	})
	if err != nil {
		s.recordFailure(ctx, "data-permission.policy.publish", policyID, policy)
		return PolicyRecord{}, err
	}
	if s.runtime != nil {
		if err := s.runtime.Changed(ctx); err != nil {
			return PolicyRecord{}, fmt.Errorf("refresh published data permission policies: %w", err)
		}
	}
	record, _, err := s.get(ctx, policyID, number, actor.TenantID)
	return record, err
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
		return s.recordChangeTx(ctx, tx, "data-permission.policy.status.set", input.PolicyID, policy, 0)
	})
	if err != nil {
		s.recordFailure(ctx, "data-permission.policy.status.set", input.PolicyID, policy)
		return PolicyRecord{}, err
	}
	if s.runtime != nil {
		if err := s.runtime.Changed(ctx); err != nil {
			return PolicyRecord{}, fmt.Errorf("refresh published data permission policies: %w", err)
		}
	}
	return s.repository.Get(ctx, input.PolicyID, actor.TenantID)
}

func (s *LifecycleService) get(ctx context.Context, id string, number int64, tenantID string) (PolicyRecord, VersionRecord, error) {
	policy, err := s.repository.Get(ctx, id, tenantID)
	if err != nil {
		return PolicyRecord{}, VersionRecord{}, err
	}
	version, err := s.repository.GetVersion(ctx, id, number, tenantID)
	return policy, version, err
}

func lockPolicy(ctx context.Context, tx *sqlx.Tx, id, tenantID string) (PolicyRecord, error) {
	var record PolicyRecord
	query, args := visiblePolicyQuery(`SELECT id,code,name,scope,tenant_id,published_version_number,status,created_at,created_by,updated_at,updated_by,version FROM data_permission_policies WHERE id=? AND deleted_at IS NULL`, []any{id}, tenantID)
	if err := tx.GetContext(ctx, &record, tx.Rebind(query+` FOR UPDATE`), args...); err != nil {
		return record, ErrPolicyNotFound
	}
	return record, nil
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
