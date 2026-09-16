package pbac

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

// LifecycleService owns policy drafts and atomic publication. It does not
// perform runtime request authorization.
type LifecycleService struct {
	repository *Repository
	transactor *database.Transactor
	registry   *Registry
	operations operationlog.TransactionalRecorder
	security   securitylog.TransactionalRecorder
	runtime    *RuntimeLoader
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

func (s *LifecycleService) GetVersion(ctx context.Context, policyID string, versionNumber int64) (PolicyVersionRecord, error) {
	actor, err := lifecycleActor(ctx)
	if err != nil {
		return PolicyVersionRecord{}, err
	}
	if policyID == "" || versionNumber <= 0 {
		return PolicyVersionRecord{}, ErrInvalidPolicy
	}
	return s.repository.GetVersion(ctx, policyID, versionNumber, actor.TenantID)
}

func (s *LifecycleService) Page(ctx context.Context, input PolicyPageInput) (PolicyPage, error) {
	actor, err := lifecycleActor(ctx)
	if err != nil {
		return PolicyPage{}, err
	}
	return s.repository.Page(ctx, actor.TenantID, input)
}

func (s *LifecycleService) PageVersions(ctx context.Context, input PolicyVersionPageInput) (PolicyVersionPage, error) {
	actor, err := lifecycleActor(ctx)
	if err != nil {
		return PolicyVersionPage{}, err
	}
	return s.repository.PageVersions(ctx, actor.TenantID, input)
}

func NewLifecycleService(
	repository *Repository,
	transactor *database.Transactor,
	registry *Registry,
	operations operationlog.TransactionalRecorder,
	security securitylog.TransactionalRecorder,
	runtime *RuntimeLoader,
) *LifecycleService {
	return &LifecycleService{
		repository: repository,
		transactor: transactor,
		registry:   registry,
		operations: operations,
		security:   security,
		runtime:    runtime,
	}
}

// Create creates a logical policy and version 1 as an unpublished draft.
func (s *LifecycleService) Create(ctx context.Context, input CreatePolicyInput) (Publication, error) {
	actor, err := lifecycleActor(ctx)
	if err != nil {
		return Publication{}, err
	}
	policy, document, err := s.prepare(input.Document)
	if err != nil {
		return Publication{}, err
	}
	if err := authorizePolicyTenant(actor, policy.Scope); err != nil {
		return Publication{}, err
	}
	now := time.Now()
	policyID := uuid.NewString()
	versionID := uuid.NewString()
	err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		tenantID := nullableTenantID(policy.Scope)
		query := tx.Rebind(`INSERT INTO pbac_policies
			(id,code,name,description,scope,tenant_id,published_version_number,status,created_at,created_by,updated_at,updated_by,version)
			VALUES(?,?,?,?,?,?,NULL,?,?,?,?,?,1)`)
		if _, err := tx.ExecContext(ctx, query, policyID, policy.Metadata.Code, policy.Metadata.Name,
			policy.Metadata.Description, policy.Scope.Type, tenantID, PolicyStatusActive,
			now, actor.ID, now, actor.ID); err != nil {
			if database.IsUniqueViolation(err) {
				return ErrPolicyConflict
			}
			return fmt.Errorf("insert pbac policy: %w", err)
		}
		if err := insertPolicyVersion(ctx, tx, versionID, policyID, 1, document, actor.ID, now); err != nil {
			return err
		}
		if err := insertPolicyActions(ctx, tx, versionID, policy, actor.ID, now); err != nil {
			return err
		}
		return s.recordChangeTx(ctx, tx, "pbac.policy.create", policyID, policy, 1, policy.Scope.TenantID)
	})
	if err != nil {
		s.recordFailure(ctx, "pbac.policy.create", policyID, policy.Metadata.Name, policy.Scope.TenantID)
		return Publication{}, err
	}
	record, err := s.repository.Get(ctx, policyID, actor.TenantID)
	if err != nil {
		return Publication{}, fmt.Errorf("read created pbac policy: %w", err)
	}
	version, err := s.repository.GetVersion(ctx, policyID, 1, actor.TenantID)
	if err != nil {
		return Publication{}, fmt.Errorf("read created pbac policy version: %w", err)
	}
	return Publication{Policy: record, Version: version}, nil
}

// CreateVersion appends an immutable draft while optimistically updating the
// logical policy display metadata.
func (s *LifecycleService) CreateVersion(ctx context.Context, input CreateVersionInput) (PolicyVersionRecord, error) {
	actor, err := lifecycleActor(ctx)
	if err != nil {
		return PolicyVersionRecord{}, err
	}
	if input.PolicyID == "" || input.ExpectedPolicyVersion <= 0 {
		return PolicyVersionRecord{}, ErrInvalidPolicy
	}
	policy, document, err := s.prepare(input.Document)
	if err != nil {
		return PolicyVersionRecord{}, err
	}
	var versionNumber int64
	versionID := uuid.NewString()
	now := time.Now()
	err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		current, err := lockPolicy(ctx, tx, input.PolicyID, actor.TenantID)
		if err != nil {
			return err
		}
		if current.Version != input.ExpectedPolicyVersion {
			return ErrPolicyConflict
		}
		if current.Status != PolicyStatusActive {
			return ErrPolicyDisabled
		}
		if err := ensureDocumentIdentity(current, policy); err != nil {
			return err
		}
		if err := authorizePolicyTenant(actor, policy.Scope); err != nil {
			return err
		}
		query := tx.Rebind("SELECT COALESCE(max(version_number),0)+1 FROM pbac_policy_versions WHERE policy_id=? AND deleted_at IS NULL")
		if err := tx.GetContext(ctx, &versionNumber, query, input.PolicyID); err != nil {
			return fmt.Errorf("allocate pbac policy version: %w", err)
		}
		if err := insertPolicyVersion(ctx, tx, versionID, input.PolicyID, versionNumber, document, actor.ID, now); err != nil {
			return err
		}
		if err := insertPolicyActions(ctx, tx, versionID, policy, actor.ID, now); err != nil {
			return err
		}
		update, args := visiblePolicyUpdate(`UPDATE pbac_policies
			SET name=?,description=?,updated_at=?,updated_by=?,version=version+1
			WHERE id=? AND version=? AND deleted_at IS NULL`,
			[]any{policy.Metadata.Name, policy.Metadata.Description, now, actor.ID, input.PolicyID, input.ExpectedPolicyVersion},
			actor.TenantID,
		)
		result, err := tx.ExecContext(ctx, tx.Rebind(update), args...)
		if err != nil {
			return fmt.Errorf("update pbac policy metadata: %w", err)
		}
		if err := requireOneRow(result); err != nil {
			return err
		}
		return s.recordChangeTx(ctx, tx, "pbac.policy.version.create", input.PolicyID, policy, versionNumber, policy.Scope.TenantID)
	})
	if err != nil {
		s.recordFailure(ctx, "pbac.policy.version.create", input.PolicyID, policy.Metadata.Name, policy.Scope.TenantID)
		return PolicyVersionRecord{}, err
	}
	return s.repository.GetVersion(ctx, input.PolicyID, versionNumber, actor.TenantID)
}

// Publish archives the previous published version and atomically publishes one
// validated draft.
func (s *LifecycleService) Publish(ctx context.Context, input PublishInput) (Publication, error) {
	actor, err := lifecycleActor(ctx)
	if err != nil {
		return Publication{}, err
	}
	if input.PolicyID == "" || input.VersionNumber <= 0 || input.ExpectedPolicyVersion <= 0 {
		return Publication{}, ErrInvalidPolicy
	}
	now := time.Now()
	var policy Policy
	err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		current, err := lockPolicy(ctx, tx, input.PolicyID, actor.TenantID)
		if err != nil {
			return err
		}
		if current.Version != input.ExpectedPolicyVersion {
			return ErrPolicyConflict
		}
		if current.Status != PolicyStatusActive {
			return ErrPolicyDisabled
		}
		version, err := getVersionTx(ctx, tx, input.PolicyID, input.VersionNumber)
		if err != nil {
			return err
		}
		if version.Status != VersionStatusDraft {
			return ErrVersionNotDraft
		}
		policy, err = ParsePolicy([]byte(version.Document))
		if err != nil {
			return err
		}
		if err := ensureDocumentIdentity(current, policy); err != nil {
			return err
		}
		if err := authorizePolicyTenant(actor, policy.Scope); err != nil {
			return err
		}
		if err := policy.Validate(s.registry); err != nil {
			return err
		}
		archive := tx.Rebind(`UPDATE pbac_policy_versions
			SET status='archived',updated_at=?,updated_by=?,version=version+1
			WHERE policy_id=? AND status='published' AND deleted_at IS NULL`)
		if _, err := tx.ExecContext(ctx, archive, now, actor.ID, input.PolicyID); err != nil {
			return fmt.Errorf("archive published pbac policy version: %w", err)
		}
		publish := tx.Rebind(`UPDATE pbac_policy_versions
			SET status='published',published_at=?,published_by=?,updated_at=?,updated_by=?,version=version+1
			WHERE id=? AND policy_id=? AND version_number=? AND status='draft' AND deleted_at IS NULL`)
		result, err := tx.ExecContext(ctx, publish, now, actor.ID, now, actor.ID, version.ID, input.PolicyID, input.VersionNumber)
		if err != nil {
			return fmt.Errorf("publish pbac policy version: %w", err)
		}
		if err := requireOneRow(result); err != nil {
			return ErrPolicyConflict
		}
		update, args := visiblePolicyUpdate(`UPDATE pbac_policies
			SET published_version_number=?,updated_at=?,updated_by=?,version=version+1
			WHERE id=? AND version=? AND status='active' AND deleted_at IS NULL`,
			[]any{input.VersionNumber, now, actor.ID, input.PolicyID, input.ExpectedPolicyVersion},
			actor.TenantID,
		)
		result, err = tx.ExecContext(ctx, tx.Rebind(update), args...)
		if err != nil {
			return fmt.Errorf("switch published pbac policy version: %w", err)
		}
		if err := requireOneRow(result); err != nil {
			return err
		}
		return s.recordChangeTx(ctx, tx, "pbac.policy.publish", input.PolicyID, policy, input.VersionNumber, policy.Scope.TenantID)
	})
	if err != nil {
		s.recordFailure(ctx, "pbac.policy.publish", input.PolicyID, policy.Metadata.Name, policy.Scope.TenantID)
		return Publication{}, err
	}
	record, err := s.repository.Get(ctx, input.PolicyID, actor.TenantID)
	if err != nil {
		return Publication{}, fmt.Errorf("read published pbac policy: %w", err)
	}
	version, err := s.repository.GetVersion(ctx, input.PolicyID, input.VersionNumber, actor.TenantID)
	if err != nil {
		return Publication{}, fmt.Errorf("read published pbac policy version: %w", err)
	}
	if s.runtime != nil {
		if err := s.runtime.Changed(ctx); err != nil {
			return Publication{}, fmt.Errorf("refresh published pbac policies: %w", err)
		}
	}
	return Publication{Policy: record, Version: version}, nil
}

// SetStatus enables or disables a policy without mutating any immutable
// version document.
func (s *LifecycleService) SetStatus(ctx context.Context, input SetPolicyStatusInput) (PolicyRecord, error) {
	actor, err := lifecycleActor(ctx)
	if err != nil {
		return PolicyRecord{}, err
	}
	if input.PolicyID == "" || input.ExpectedPolicyVersion <= 0 ||
		(input.Status != PolicyStatusActive && input.Status != PolicyStatusDisabled) {
		return PolicyRecord{}, ErrInvalidPolicy
	}
	now := time.Now()
	var policy Policy
	err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		current, err := lockPolicy(ctx, tx, input.PolicyID, actor.TenantID)
		if err != nil {
			return err
		}
		if current.Version != input.ExpectedPolicyVersion {
			return ErrPolicyConflict
		}
		scope := PolicyScope{Type: current.Scope}
		if current.TenantID != nil {
			scope.TenantID = *current.TenantID
		}
		if err := authorizePolicyTenant(actor, scope); err != nil {
			return err
		}
		policy = Policy{
			Metadata: PolicyMetadata{Code: current.Code, Name: current.Name, Description: current.Description},
			Scope:    scope,
		}
		update, args := visiblePolicyUpdate(
			"UPDATE pbac_policies SET status=?,updated_at=?,updated_by=?,version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL",
			[]any{input.Status, now, actor.ID, input.PolicyID, input.ExpectedPolicyVersion},
			actor.TenantID,
		)
		result, err := tx.ExecContext(ctx, tx.Rebind(update), args...)
		if err != nil {
			return fmt.Errorf("update pbac policy status: %w", err)
		}
		if err := requireOneRow(result); err != nil {
			return err
		}
		return s.recordChangeTx(ctx, tx, "pbac.policy.status.set", input.PolicyID, policy, 0, scope.TenantID)
	})
	if err != nil {
		s.recordFailure(ctx, "pbac.policy.status.set", input.PolicyID, policy.Metadata.Name, policy.Scope.TenantID)
		return PolicyRecord{}, err
	}
	record, err := s.repository.Get(ctx, input.PolicyID, actor.TenantID)
	if err != nil {
		return PolicyRecord{}, err
	}
	if s.runtime != nil {
		if err := s.runtime.Changed(ctx); err != nil {
			return PolicyRecord{}, fmt.Errorf("refresh published pbac policies: %w", err)
		}
	}
	return record, nil
}

func (s *LifecycleService) prepare(source Policy) (Policy, string, error) {
	policy := clonePolicy(source)
	policy.normalize()
	if err := policy.Validate(s.registry); err != nil {
		return Policy{}, "", err
	}
	document, err := MarshalPolicy(policy)
	if err != nil {
		return Policy{}, "", err
	}
	return policy, string(document), nil
}

func insertPolicyVersion(ctx context.Context, tx *sqlx.Tx, id, policyID string, versionNumber int64, document, actor string, now time.Time) error {
	query := tx.Rebind(`INSERT INTO pbac_policy_versions
		(id,policy_id,version_number,document,status,published_at,published_by,created_at,created_by,updated_at,updated_by,version)
		VALUES(?,?,?,?,?,NULL,NULL,?,?,?,?,1)`)
	if _, err := tx.ExecContext(ctx, query, id, policyID, versionNumber, document, VersionStatusDraft, now, actor, now, actor); err != nil {
		if database.IsUniqueViolation(err) {
			return ErrPolicyConflict
		}
		return fmt.Errorf("insert pbac policy version: %w", err)
	}
	return nil
}

func insertPolicyActions(ctx context.Context, tx *sqlx.Tx, versionID string, policy Policy, actor string, now time.Time) error {
	query := tx.Rebind(`INSERT INTO pbac_policy_actions
		(id,policy_version_id,resource,action,created_at,created_by,updated_at,updated_by,version)
		VALUES(?,?,?,?,?,?,?,?,1)`)
	for _, action := range policy.Spec.Actions {
		if _, err := tx.ExecContext(ctx, query, uuid.NewString(), versionID, policy.Spec.Resource.Type, action, now, actor, now, actor); err != nil {
			return fmt.Errorf("insert pbac policy action: %w", err)
		}
	}
	return nil
}

func ensureDocumentIdentity(record PolicyRecord, policy Policy) error {
	tenantID := ""
	if record.TenantID != nil {
		tenantID = *record.TenantID
	}
	if record.Code != policy.Metadata.Code || record.Scope != policy.Scope.Type || tenantID != policy.Scope.TenantID {
		return fmt.Errorf("%w: policy code and scope are immutable", ErrInvalidPolicy)
	}
	return nil
}

func authorizePolicyTenant(actor platformprincipal.Principal, scope PolicyScope) error {
	switch scope.Type {
	case PolicyScopeGlobal:
		if actor.TenantID != "" {
			return ErrTenantAccessDenied
		}
	case PolicyScopeTenant:
		if actor.TenantID == "" || actor.TenantID != scope.TenantID {
			return ErrTenantAccessDenied
		}
	default:
		return ErrInvalidPolicy
	}
	return nil
}

func lifecycleActor(ctx context.Context) (platformprincipal.Principal, error) {
	actor, ok := platformprincipal.FromContext(ctx)
	if !ok || actor.ID == "" {
		return platformprincipal.Principal{}, platformprincipal.ErrMissing
	}
	return actor, nil
}

func nullableTenantID(scope PolicyScope) any {
	if scope.Type == PolicyScopeTenant {
		return scope.TenantID
	}
	return nil
}

func visiblePolicyUpdate(base string, args []any, tenantID string) (string, []any) {
	if tenantID == "" {
		return base + " AND scope='global'", args
	}
	return base + " AND (scope='global' OR (scope='tenant' AND tenant_id=?))", append(args, tenantID)
}

func requireOneRow(result interface{ RowsAffected() (int64, error) }) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read pbac write result: %w", err)
	}
	if affected != 1 {
		return ErrPolicyConflict
	}
	return nil
}

func (s *LifecycleService) recordChangeTx(ctx context.Context, tx *sqlx.Tx, operation, policyID string, policy Policy, versionNumber int64, tenantID string) error {
	if s.operations != nil {
		entry := operationlog.Entry{
			Operation: operation, ResourceType: "pbac_policy", ResourceID: policyID,
			ResourceName: policy.Metadata.Name, Source: "backend", Protocol: "service",
			Request:   map[string]any{"policy_code": policy.Metadata.Code, "version_number": versionNumber},
			Succeeded: true,
		}
		if err := s.operations.RecordTx(ctx, tx, entry); err != nil {
			return err
		}
	}
	if s.security != nil {
		entry := securitylog.Entry{
			EventType: securitylog.EventPBACPolicyChanged, SubjectID: policyID,
			SubjectName: policy.Metadata.Name, SubjectType: "pbac_policy", TenantID: tenantID,
			Succeeded: true, Metadata: map[string]any{"operation": operation, "version_number": versionNumber},
		}
		if err := s.security.RecordTx(ctx, tx, entry); err != nil {
			return err
		}
	}
	return nil
}

func (s *LifecycleService) recordFailure(ctx context.Context, operation, policyID, policyName, tenantID string) {
	if s.operations != nil {
		_ = s.operations.Record(ctx, operationlog.Entry{
			Operation: operation, ResourceType: "pbac_policy", ResourceID: policyID,
			ResourceName: policyName, Source: "backend", Protocol: "service",
			Succeeded: false, ErrorCode: "operation_failed", ErrorMessage: "operation failed",
		})
	}
	if s.security != nil {
		_ = s.security.Record(ctx, securitylog.Entry{
			EventType: securitylog.EventPBACPolicyChanged, SubjectID: policyID,
			SubjectName: policyName, SubjectType: "pbac_policy", TenantID: tenantID,
			Succeeded: false, ErrorCode: "operation_failed", ErrorMessage: "operation failed",
			Metadata: map[string]any{"operation": operation},
		})
	}
}
