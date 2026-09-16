package pbac

import (
	"database/sql"
	"errors"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/pagination"
)

var (
	ErrPolicyNotFound       = errors.New("pbac: policy not found")
	ErrPolicyVersionMissing = errors.New("pbac: policy version not found")
	ErrPolicyConflict       = errors.New("pbac: policy version conflict")
	ErrPolicyDisabled       = errors.New("pbac: policy is disabled")
	ErrTenantAccessDenied   = errors.New("pbac: tenant policy access denied")
	ErrVersionNotDraft      = errors.New("pbac: policy version is not draft")
)

const (
	PolicyStatusActive   = "active"
	PolicyStatusDisabled = "disabled"

	VersionStatusDraft     = "draft"
	VersionStatusPublished = "published"
	VersionStatusArchived  = "archived"
)

// PolicyRecord is the stable identity and current publication pointer of a
// policy. Version is the optimistic-lock value maintained by audit triggers.
type PolicyRecord struct {
	ID                     string          `db:"id" json:"id"`
	Code                   string          `db:"code" json:"code"`
	Name                   string          `db:"name" json:"name"`
	Description            string          `db:"description" json:"description"`
	Scope                  PolicyScopeType `db:"scope" json:"scope"`
	TenantID               *string         `db:"tenant_id" json:"tenant_id,omitempty"`
	PublishedVersionNumber *int64          `db:"published_version_number" json:"published_version_number,omitempty"`
	Status                 string          `db:"status" json:"status"`
	CreatedAt              time.Time       `db:"created_at" json:"created_at"`
	CreatedBy              string          `db:"created_by" json:"created_by"`
	UpdatedAt              time.Time       `db:"updated_at" json:"updated_at"`
	UpdatedBy              string          `db:"updated_by" json:"updated_by"`
	Version                int64           `db:"version" json:"version"`
}

// PolicyVersionRecord describes one immutable policy document revision.
type PolicyVersionRecord struct {
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

// CreatePolicyInput creates a logical policy and its first draft version.
type CreatePolicyInput struct {
	Document Policy
}

// CreateVersionInput appends a draft version and optimistically updates the
// logical policy metadata.
type CreateVersionInput struct {
	PolicyID              string
	ExpectedPolicyVersion int64
	Document              Policy
}

// PublishInput atomically switches a policy to a validated draft version.
type PublishInput struct {
	PolicyID              string
	VersionNumber         int64
	ExpectedPolicyVersion int64
}

// SetPolicyStatusInput enables or disables a logical policy using optimistic
// locking. Disabling removes it from subsequent published loads.
type SetPolicyStatusInput struct {
	PolicyID              string
	Status                string
	ExpectedPolicyVersion int64
}

// Publication is returned after a successful version switch.
type Publication struct {
	Policy  PolicyRecord
	Version PolicyVersionRecord
}

type PolicyPageInput struct {
	pagination.Request
	Scopes   []PolicyScopeType
	Statuses []string
}

type PolicyVersionPageInput struct {
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

type PolicyVersionPage struct {
	Items    []PolicyVersionRecord `json:"items"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"page_size"`
	Total    int64                 `json:"total"`
}
