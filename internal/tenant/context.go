package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/presentation"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type AvailableTenant struct {
	TenantID        string    `db:"tenant_id" json:"tenant_id"`
	TenantCode      string    `db:"tenant_code" json:"tenant_code"`
	TenantName      string    `db:"tenant_name" json:"tenant_name"`
	MembershipID    string    `db:"membership_id" json:"membership_id"`
	IsAdministrator bool      `db:"is_administrator" json:"is_administrator"`
	JoinedAt        time.Time `db:"joined_at" json:"joined_at"`
}
type ContextToken struct {
	AccessToken string          `json:"access_token"`
	TokenType   string          `json:"token_type"`
	ExpiresIn   int64           `json:"expires_in"`
	Tenant      AvailableTenant `json:"tenant"`
}

type AvailablePageInput struct {
	pagination.Request
	TenantIDs []string
}

var ErrAvailableTooLarge = errors.New("available tenant list exceeds 1000 items; use available/page")

const availableTenantColumns = `m.tenant_id,t.code tenant_code,t.name tenant_name,m.id membership_id,CASE WHEN a.id IS NULL THEN false ELSE true END is_administrator,m.joined_at`
const availableTenantFrom = ` FROM tenant_memberships m JOIN tenants t ON t.id=m.tenant_id AND t.status='active' AND t.deleted_at IS NULL LEFT JOIN tenant_administrators a ON a.tenant_id=m.tenant_id AND a.membership_id=m.id AND a.deleted_at IS NULL`
const availableTenantWhere = ` WHERE m.user_id=? AND m.status='active' AND m.deleted_at IS NULL`

type ContextService struct {
	db       *sqlx.DB
	signer   *auth.Service
	cfg      config.Config
	security securitylog.Recorder
}

func NewContextService(db *sqlx.DB, signer *auth.Service, cfg config.Config, security securitylog.Recorder) *ContextService {
	return &ContextService{db: db, signer: signer, cfg: cfg, security: security}
}
func (s *ContextService) Available(ctx context.Context) ([]AvailableTenant, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.Type != platformprincipal.TypeUser {
		return nil, ErrForbidden
	}
	items := []AvailableTenant{}
	query := `SELECT ` + availableTenantColumns + availableTenantFrom + availableTenantWhere + ` ORDER BY t.name,t.id LIMIT ?`
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(query), actor.ID, 1001); err != nil {
		return nil, err
	}
	if len(items) > 1000 {
		return nil, ErrAvailableTooLarge
	}
	for index := range items {
		items[index] = presentAvailableTenant(items[index])
	}
	return items, nil
}

func (s *ContextService) AvailablePage(ctx context.Context, input AvailablePageInput) (pagination.Result[AvailableTenant], error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.Type != platformprincipal.TypeUser {
		return pagination.Result[AvailableTenant]{}, ErrForbidden
	}
	request, err := pagination.Normalize(input.Request)
	if err != nil || len(request.Keyword) > 256 || len(input.TenantIDs) > 200 || !boundedStrings(input.TenantIDs, maxTenantIDLength) {
		return pagination.Result[AvailableTenant]{}, ErrInvalid
	}
	where := availableTenantWhere
	args := []any{actor.ID}
	if keyword := strings.TrimSpace(request.Keyword); keyword != "" {
		where += ` AND (LOWER(t.code) LIKE ? ESCAPE '!' OR LOWER(t.name) LIKE ? ESCAPE '!')`
		pattern := database.LikeContainsPattern(strings.ToLower(keyword))
		args = append(args, pattern, pattern)
	}
	if len(input.TenantIDs) > 0 {
		where += ` AND m.tenant_id IN (?)`
		args = append(args, input.TenantIDs)
	}
	where, args, err = sqlx.In(where, args...)
	if err != nil {
		return pagination.Result[AvailableTenant]{}, ErrInvalid
	}
	var total int64
	if err := s.db.GetContext(ctx, &total, s.db.Rebind(`SELECT count(*)`+availableTenantFrom+where), args...); err != nil {
		return pagination.Result[AvailableTenant]{}, err
	}
	items := []AvailableTenant{}
	pageArgs := append(append([]any{}, args...), request.PageSize, pagination.Offset(request))
	query := `SELECT ` + availableTenantColumns + availableTenantFrom + where + ` ORDER BY t.name,t.id LIMIT ? OFFSET ?`
	if err := s.db.SelectContext(ctx, &items, s.db.Rebind(query), pageArgs...); err != nil {
		return pagination.Result[AvailableTenant]{}, err
	}
	for index := range items {
		items[index] = presentAvailableTenant(items[index])
	}
	return pagination.Result[AvailableTenant]{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total}, nil
}
func (s *ContextService) Switch(ctx context.Context, tenantID string) (ContextToken, error) {
	tenantID = strings.TrimSpace(tenantID)
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.Type != platformprincipal.TypeUser || actor.SessionID == "" || tenantID == "" || len(tenantID) > maxTenantIDLength {
		return ContextToken{}, s.recordSwitch(ctx, actor, tenantID, ErrForbidden)
	}
	var selected AvailableTenant
	query := `SELECT m.tenant_id,t.code tenant_code,t.name tenant_name,m.id membership_id,CASE WHEN a.id IS NULL THEN false ELSE true END is_administrator,m.joined_at FROM tenant_memberships m JOIN tenants t ON t.id=m.tenant_id AND t.status='active' AND t.deleted_at IS NULL LEFT JOIN tenant_administrators a ON a.tenant_id=m.tenant_id AND a.membership_id=m.id AND a.deleted_at IS NULL WHERE m.tenant_id=? AND m.user_id=? AND m.status='active' AND m.deleted_at IS NULL`
	err = s.db.GetContext(ctx, &selected, s.db.Rebind(query), tenantID, actor.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return ContextToken{}, s.recordSwitch(ctx, actor, tenantID, ErrForbidden)
	}
	if err != nil {
		return ContextToken{}, s.recordSwitch(ctx, actor, tenantID, err)
	}
	raw, err := s.signer.IssuePrincipal(platformprincipal.Principal{ID: actor.ID, Type: platformprincipal.TypeUser, SessionID: actor.SessionID, TenantID: selected.TenantID, MembershipID: selected.MembershipID})
	if err != nil {
		return ContextToken{}, s.recordSwitch(ctx, actor, tenantID, err)
	}
	if err := s.recordSwitch(ctx, actor, tenantID, nil); err != nil {
		return ContextToken{}, err
	}
	return ContextToken{AccessToken: raw, TokenType: "Bearer", ExpiresIn: int64(s.cfg.JWT.TTL.Seconds()), Tenant: presentAvailableTenant(selected)}, nil
}

func (s *ContextService) recordSwitch(ctx context.Context, actor platformprincipal.Principal, tenantID string, switchErr error) error {
	if s.security == nil {
		return switchErr
	}
	auditTenantID := tenantID
	if len(auditTenantID) > maxTenantIDLength || !utf8.ValidString(auditTenantID) {
		auditTenantID = ""
	}
	entry := securitylog.Entry{EventType: securitylog.EventTenantContextSwitch, SubjectID: actor.ID, SubjectType: string(actor.Type), TenantID: auditTenantID, SessionID: actor.SessionID, Succeeded: switchErr == nil}
	if switchErr != nil {
		entry.ErrorCode = "tenant_context_switch_failed"
		entry.ErrorMessage = "tenant context switch failed"
	}
	if err := s.security.Record(ctx, entry); err != nil && s.security.FailClosed() {
		return ErrSecurityUnavailable
	}
	return switchErr
}
func (s *ContextService) Current(ctx context.Context) (AvailableTenant, error) {
	actor, err := platformprincipal.Require(ctx)
	if err != nil || actor.TenantID == "" || actor.MembershipID == "" {
		return AvailableTenant{}, ErrForbidden
	}
	var selected AvailableTenant
	query := `SELECT m.tenant_id,t.code tenant_code,t.name tenant_name,m.id membership_id,CASE WHEN a.id IS NULL THEN false ELSE true END is_administrator,m.joined_at FROM tenant_memberships m JOIN tenants t ON t.id=m.tenant_id AND t.status='active' AND t.deleted_at IS NULL LEFT JOIN tenant_administrators a ON a.tenant_id=m.tenant_id AND a.membership_id=m.id AND a.deleted_at IS NULL WHERE m.tenant_id=? AND m.id=? AND m.user_id=? AND m.status='active' AND m.deleted_at IS NULL`
	if err := s.db.GetContext(ctx, &selected, s.db.Rebind(query), actor.TenantID, actor.MembershipID, actor.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AvailableTenant{}, ErrForbidden
		}
		return AvailableTenant{}, fmt.Errorf("load current tenant: %w", err)
	}
	return presentAvailableTenant(selected), nil
}

func presentAvailableTenant(value AvailableTenant) AvailableTenant {
	value.JoinedAt = presentation.Time(value.JoinedAt)
	return value
}
