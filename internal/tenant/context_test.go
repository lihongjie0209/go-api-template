package tenant

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/auth"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/stretchr/testify/require"
)

func TestContextService_AvailableScopesMembershipsToAuthenticatedUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sqlxDB := sqlx.NewDb(db, "sqlmock")
	service := &ContextService{db: sqlxDB}
	mock.ExpectQuery(`FROM tenant_memberships m JOIN tenants`).WithArgs("user-1", 1001).WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "tenant_code", "tenant_name", "membership_id", "is_administrator", "joined_at"}))
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	items, err := service.Available(ctx)
	require.NoError(t, err)
	require.NotNil(t, items)
	require.Empty(t, items)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContextServicePresentsJoinedAtInPlatformTimezone(t *testing.T) {
	joined := time.Date(2026, time.October, 1, 2, 3, 4, 0, time.UTC)
	columns := []string{"tenant_id", "tenant_code", "tenant_name", "membership_id", "is_administrator", "joined_at"}
	newService := func(t *testing.T) (*ContextService, sqlmock.Sqlmock) {
		t.Helper()
		raw, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = raw.Close() })
		cfg := contextSigningConfig(t)
		return NewContextService(sqlx.NewDb(raw, "sqlmock"), auth.New(cfg), cfg, nil), mock
	}
	t.Run("available", func(t *testing.T) {
		service, mock := newService(t)
		mock.ExpectQuery(`FROM tenant_memberships m JOIN tenants`).WithArgs("user-1", 1001).
			WillReturnRows(sqlmock.NewRows(columns).AddRow("tenant-1", "tenant", "Tenant", "member-1", false, joined))
		ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
		items, err := service.Available(ctx)
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.Equal(t, "2026-10-01T10:03:04+08:00", items[0].JoinedAt.Format(time.RFC3339))
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("switch", func(t *testing.T) {
		service, mock := newService(t)
		mock.ExpectQuery(`FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "user-1").
			WillReturnRows(sqlmock.NewRows(columns).AddRow("tenant-1", "tenant", "Tenant", "member-1", false, joined))
		ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"})
		token, err := service.Switch(ctx, "tenant-1")
		require.NoError(t, err)
		require.NotEmpty(t, token.AccessToken)
		require.Equal(t, "2026-10-01T10:03:04+08:00", token.Tenant.JoinedAt.Format(time.RFC3339))
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("current", func(t *testing.T) {
		service, mock := newService(t)
		mock.ExpectQuery(`FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "member-1", "user-1").
			WillReturnRows(sqlmock.NewRows(columns).AddRow("tenant-1", "tenant", "Tenant", "member-1", false, joined))
		ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"})
		item, err := service.Current(ctx)
		require.NoError(t, err)
		require.Equal(t, "2026-10-01T10:03:04+08:00", item.JoinedAt.Format(time.RFC3339))
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestContextServiceAvailablePageScopesAndEscapesKeyword(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &ContextService{db: sqlx.NewDb(raw, "sqlmock")}
	joined := time.Date(2026, time.October, 1, 2, 3, 4, 0, time.UTC)
	mock.ExpectQuery(`SELECT count\(\*\) FROM tenant_memberships m JOIN tenants t ON t.id=m.tenant_id AND t.status='active' AND t.deleted_at IS NULL.*WHERE m.user_id=\? AND m.status='active' AND m.deleted_at IS NULL`).
		WithArgs("user-1", "%a!%b%", "%a!%b%", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(21))
	mock.ExpectQuery(`SELECT m.tenant_id,t.code tenant_code.*WHERE m.user_id=\? AND m.status='active' AND m.deleted_at IS NULL`).
		WithArgs("user-1", "%a!%b%", "%a!%b%", "tenant-1", 20, 20).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "tenant_code", "tenant_name", "membership_id", "is_administrator", "joined_at"}).
			AddRow("tenant-1", "a%b", "Tenant A", "member-1", false, joined))
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	page, err := service.AvailablePage(ctx, AvailablePageInput{Request: pagination.Request{Page: 2, PageSize: 20, Keyword: " a%b "}, TenantIDs: []string{"tenant-1"}})
	require.NoError(t, err)
	require.Equal(t, int64(21), page.Total)
	require.Equal(t, 2, page.Page)
	require.Equal(t, 20, page.PageSize)
	require.Len(t, page.Items, 1)
	require.Equal(t, "2026-10-01T10:03:04+08:00", page.Items[0].JoinedAt.Format(time.RFC3339))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContextServiceAvailablePageRejectsInvalidFiltersBeforeSQL(t *testing.T) {
	service := &ContextService{}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	for _, input := range []AvailablePageInput{
		{Request: pagination.Request{PageSize: pagination.MaxPageSize + 1}},
		{Request: pagination.Request{Keyword: strings.Repeat("x", 257)}},
		{TenantIDs: []string{""}},
		{TenantIDs: []string{strings.Repeat("x", maxTenantIDLength+1)}},
	} {
		_, err := service.AvailablePage(ctx, input)
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestContextServiceAvailableFailsWhenResultExceedsLegacyBound(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &ContextService{db: sqlx.NewDb(raw, "sqlmock")}
	rows := sqlmock.NewRows([]string{"tenant_id", "tenant_code", "tenant_name", "membership_id", "is_administrator", "joined_at"})
	for index := 0; index <= 1000; index++ {
		rows.AddRow(fmt.Sprintf("tenant-%d", index), "tenant", "Tenant", "member", false, time.Now())
	}
	mock.ExpectQuery(`FROM tenant_memberships m JOIN tenants`).WithArgs("user-1", 1001).WillReturnRows(rows)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	_, err = service.Available(ctx)
	require.ErrorIs(t, err, ErrAvailableTooLarge)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContextService_CurrentRequiresActiveTenantAndMembership(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service := &ContextService{db: sqlx.NewDb(db, "sqlmock")}
	mock.ExpectQuery(`JOIN tenants t ON t.id=m.tenant_id AND t.status='active'.*WHERE m.tenant_id=\? AND m.id=\? AND m.user_id=\? AND m.status='active'`).WithArgs("tenant-1", "membership-1", "user-1").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "tenant_code", "tenant_name", "membership_id", "is_administrator", "joined_at"}))
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "membership-1"})
	_, err = service.Current(ctx)
	require.ErrorIs(t, err, ErrForbidden)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContextService_CurrentPropagatesMembershipLookupFailure(t *testing.T) {
	t.Parallel()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	service := &ContextService{db: sqlx.NewDb(raw, "sqlmock")}
	mock.ExpectQuery(`FROM tenant_memberships m JOIN tenants`).
		WithArgs("tenant-1", "membership-1", "user-1").
		WillReturnError(context.DeadlineExceeded)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "membership-1"})
	_, err = service.Current(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, ErrForbidden)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContextService_AvailableRejectsServiceAccount(t *testing.T) {
	service := &ContextService{}
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "service-1", Type: platformprincipal.TypeServiceAccount})
	_, err := service.Available(ctx)
	require.ErrorIs(t, err, ErrForbidden)
}

func TestContextService_SwitchRecordsRejectedAttemptAtServiceBoundary(t *testing.T) {
	security := &transactionalSecurityRecorderStub{}
	service := &ContextService{security: security}
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "service-1", Type: platformprincipal.TypeServiceAccount})

	_, err := service.Switch(ctx, "tenant-1")
	require.ErrorIs(t, err, ErrForbidden)
	require.Len(t, security.standalone, 1)
	entry := security.standalone[0]
	require.Equal(t, securitylog.EventTenantContextSwitch, entry.EventType)
	require.Equal(t, "service-1", entry.SubjectID)
	require.Equal(t, "tenant-1", entry.TenantID)
	require.False(t, entry.Succeeded)
}

func TestContextService_SwitchBoundsRejectedTargetBeforeSecurityAudit(t *testing.T) {
	security := &transactionalSecurityRecorderStub{}
	service := &ContextService{security: security}
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"})
	target := strings.Repeat("x", maxTenantIDLength+1)
	_, err := service.Switch(ctx, target)
	require.ErrorIs(t, err, ErrForbidden)
	require.Len(t, security.standalone, 1)
	require.Empty(t, security.standalone[0].TenantID)
	require.False(t, security.standalone[0].Succeeded)
}

func TestContextService_SwitchFailsClosedWhenSecurityAuditIsUnavailable(t *testing.T) {
	wantErr := errors.New("security store password=secret-value unavailable")
	security := &transactionalSecurityRecorderStub{recordErr: wantErr}
	service := &ContextService{security: security}
	ctx := platformprincipal.WithContext(context.Background(), platformprincipal.Principal{ID: "service-1", Type: platformprincipal.TypeServiceAccount})

	_, err := service.Switch(ctx, "tenant-1")
	require.ErrorIs(t, err, ErrSecurityUnavailable)
	require.NotErrorIs(t, err, ErrForbidden)
	require.NotContains(t, err.Error(), "secret-value")
}

func TestContextService_SwitchDoesNotReturnIssuedTokenWhenAuditFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now()
	mock.ExpectQuery(`FROM tenant_memberships m JOIN tenants`).WithArgs("tenant-1", "user-1").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "tenant_code", "tenant_name", "membership_id", "is_administrator", "joined_at"}).AddRow("tenant-1", "tenant", "Tenant", "membership-1", true, now))
	wantErr := errors.New("security store unavailable")
	security := &transactionalSecurityRecorderStub{recordErr: wantErr}
	cfg := contextSigningConfig(t)
	service := NewContextService(sqlx.NewDb(db, "sqlmock"), auth.New(cfg), cfg, security)
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"})

	token, err := service.Switch(ctx, "tenant-1")
	require.ErrorIs(t, err, ErrSecurityUnavailable)
	require.Empty(t, token.AccessToken)
	require.Len(t, security.standalone, 1)
	require.True(t, security.standalone[0].Succeeded)
	require.NoError(t, mock.ExpectationsWereMet())
}

func contextSigningConfig(t *testing.T) config.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	return config.Config{JWT: config.JWT{Issuer: "identity", Audience: "platform", Algorithm: "ES256", KeyID: "test-key", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})), TTL: time.Hour}}
}
