package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/testutil"
	"github.com/lihongjie0209/microservice-platform-go/authn"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type verifierFunc func(context.Context, string) (platformprincipal.Principal, error)

func (f verifierFunc) VerifyBearer(ctx context.Context, raw string) (platformprincipal.Principal, error) {
	return f(ctx, raw)
}

func (verifierFunc) Close() {}

func TestService_ES256IssueParseAndJWKS(t *testing.T) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{JWT: config.JWT{Issuer: "issuer", Audience: "audience", Algorithm: "ES256", KeyID: "ec-2026-09", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})), TTL: time.Hour}})
	raw, err := service.Issue("user-1")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := service.Parse(raw)
	if err != nil || claims.Subject != "user-1" {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}
	keys := service.JWKS().Keys
	if len(keys) != 1 || keys[0].KTY != "EC" || keys[0].CRV != "P-256" || keys[0].KID != "ec-2026-09" || keys[0].X == "" || keys[0].Y == "" {
		t.Fatalf("jwks=%+v", keys)
	}
	point, err := private.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if keys[0].X != base64.RawURLEncoding.EncodeToString(point[1:33]) || keys[0].Y != base64.RawURLEncoding.EncodeToString(point[33:]) {
		t.Fatalf("jwks coordinates do not match signing key")
	}
}

func TestService_VerifyRejectsSessionWhenIdentityUserIsDisabled(t *testing.T) {
	t.Parallel()
	jwtConfig, err := testutil.JWTConfig()
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{JWT: jwtConfig})
	raw, err := service.IssuePrincipal(platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	service.db = sqlx.NewDb(db, "sqlmock")
	service.verifier = new(authn.JWKSVerifier)
	mock.ExpectQuery(`SELECT c.must_change_password FROM identity_sessions s JOIN identity_users u`).WithArgs("session-1", "user-1", sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"must_change_password"}))
	if _, err := service.Verify(context.Background(), raw); err == nil {
		t.Fatal("Verify() accepted a session for a disabled user")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
}

func TestService_VerifyUsesCurrentCredentialPasswordRequirement(t *testing.T) {
	for _, test := range []struct {
		name       string
		claimState bool
		stored     bool
	}{
		{name: "newly required", stored: true},
		{name: "requirement cleared", claimState: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			jwtConfig, err := testutil.JWTConfig()
			if err != nil {
				t.Fatal(err)
			}
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			service := NewWithDatabase(config.Config{JWT: jwtConfig}, sqlx.NewDb(db, "sqlmock"))
			raw, err := service.IssuePrincipalWithState(
				platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"},
				TokenState{MustChangePassword: test.claimState},
			)
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectQuery(`SELECT c.must_change_password FROM identity_sessions s JOIN identity_users u.*JOIN identity_user_credentials c`).
				WithArgs("session-1", "user-1", sqlmock.AnyArg()).
				WillReturnRows(sqlmock.NewRows([]string{"must_change_password"}).AddRow(test.stored))
			_, state, err := service.VerifyWithState(t.Context(), raw)
			if err != nil {
				t.Fatal(err)
			}
			if state.MustChangePassword != test.stored {
				t.Fatalf("MustChangePassword = %v, want %v", state.MustChangePassword, test.stored)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestService_VerifyRejectsSessionWithoutActiveCredential(t *testing.T) {
	t.Parallel()
	jwtConfig, err := testutil.JWTConfig()
	if err != nil {
		t.Fatal(err)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service := NewWithDatabase(config.Config{JWT: jwtConfig}, sqlx.NewDb(db, "sqlmock"))
	raw, err := service.IssuePrincipal(platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`SELECT c.must_change_password FROM identity_sessions s.*JOIN identity_user_credentials c ON c.user_id=s.user_id AND c.deleted_at IS NULL`).
		WithArgs("session-1", "user-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"must_change_password"}))
	if _, err := service.Verify(t.Context(), raw); err == nil {
		t.Fatal("Verify accepted a session without an active credential")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_VerifyRejectsDisabledServiceAccount(t *testing.T) {
	t.Parallel()
	jwtConfig, err := testutil.JWTConfig()
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{JWT: jwtConfig})
	raw, err := service.Issue("service-1")
	if err != nil {
		t.Fatal(err)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service.db = sqlx.NewDb(db, "sqlmock")
	mock.ExpectQuery(`SELECT count\(\*\) FROM identity_service_accounts`).WithArgs("service-1", sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	if _, err := service.Verify(t.Context(), raw); err == nil {
		t.Fatal("Verify() accepted a disabled service account")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_VerifyDistinguishesStateStoreFailureFromInactiveToken(t *testing.T) {
	for _, test := range []struct {
		name      string
		principal platformprincipal.Principal
		query     string
		columns   []string
	}{
		{name: "user session", principal: platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"}, query: `SELECT c.must_change_password FROM identity_sessions`, columns: []string{"must_change_password"}},
		{name: "service account", principal: platformprincipal.Principal{ID: "service-1", Type: platformprincipal.TypeServiceAccount}, query: `SELECT count\(\*\) FROM identity_service_accounts`, columns: []string{"count"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			jwtConfig, err := testutil.JWTConfig()
			if err != nil {
				t.Fatal(err)
			}
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			service := NewWithDatabase(config.Config{JWT: jwtConfig}, sqlx.NewDb(db, "sqlmock"))
			raw, err := service.IssuePrincipal(test.principal)
			if err != nil {
				t.Fatal(err)
			}
			for _, failure := range []bool{true, false} {
				query := mock.ExpectQuery(test.query)
				if failure {
					query.WillReturnError(errors.New("database connection lost"))
				} else {
					query.WillReturnRows(sqlmock.NewRows(test.columns))
				}
				_, _, verifyErr := service.VerifyWithState(t.Context(), raw)
				if verifyErr == nil || errors.Is(verifyErr, ErrStateUnavailable) != failure {
					t.Fatalf("store failure=%v, VerifyWithState error=%v", failure, verifyErr)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestService_RemoteVerificationHasBoundedDeadline(t *testing.T) {
	for _, test := range []struct {
		name      string
		localKeys bool
	}{
		{name: "remote only"},
		{name: "unrecognized local key", localKeys: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.Config{Auth: config.Auth{JWKSTimeout: 20 * time.Millisecond}}
			raw := "remote-token"
			if test.localKeys {
				jwtConfig, err := testutil.JWTConfig()
				if err != nil {
					t.Fatal(err)
				}
				cfg.JWT = jwtConfig
				foreign := New(config.Config{JWT: jwtConfig})
				foreign.active.id = "foreign-key"
				raw, err = foreign.Issue("account-1")
				if err != nil {
					t.Fatal(err)
				}
			}
			service := New(cfg)
			service.verifier = verifierFunc(func(ctx context.Context, _ string) (platformprincipal.Principal, error) {
				<-ctx.Done()
				return platformprincipal.Principal{}, ctx.Err()
			})
			start := time.Now()
			_, err := service.Verify(t.Context(), raw)
			if !errors.Is(err, ErrStateUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Verify error = %v, want bounded dependency timeout", err)
			}
			if time.Since(start) > time.Second {
				t.Fatal("remote verification exceeded the bounded deadline")
			}
		})
	}
}

func TestService_RemoteInvalidTokenIsNotDependencyFailure(t *testing.T) {
	service := New(config.Config{})
	service.verifier = verifierFunc(func(context.Context, string) (platformprincipal.Principal, error) {
		return platformprincipal.Principal{}, errors.New("invalid signature")
	})
	_, err := service.Verify(t.Context(), "invalid-token")
	if err == nil || errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("Verify error = %v, want invalid-token classification", err)
	}
}

func TestService_RejectsRemoteServiceAccountTenantContext(t *testing.T) {
	service := New(config.Config{})
	service.verifier = verifierFunc(func(context.Context, string) (platformprincipal.Principal, error) {
		return platformprincipal.Principal{ID: "service-1", Type: platformprincipal.TypeServiceAccount, TenantID: "tenant-1"}, nil
	})
	if _, err := service.Verify(t.Context(), "remote-token"); err == nil || errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("Verify error = %v, want invalid service-account claims", err)
	}
}

func TestService_ValidatesRemotePrincipalShape(t *testing.T) {
	for _, test := range []struct {
		name      string
		principal platformprincipal.Principal
		wantError bool
	}{
		{name: "valid user", principal: platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"}},
		{name: "valid tenant user", principal: platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1", TenantID: "tenant-1", MembershipID: "member-1"}},
		{name: "missing user session", principal: platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser}, wantError: true},
		{name: "unpaired tenant", principal: platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1", TenantID: "tenant-1"}, wantError: true},
		{name: "unpaired membership", principal: platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1", MembershipID: "member-1"}, wantError: true},
		{name: "empty subject", principal: platformprincipal.Principal{Type: platformprincipal.TypeUser, SessionID: "session-1"}, wantError: true},
		{name: "unsupported type", principal: platformprincipal.Principal{ID: "system-1", Type: platformprincipal.TypeSystem}, wantError: true},
		{name: "valid service account", principal: platformprincipal.Principal{ID: "service-1", Type: platformprincipal.TypeServiceAccount}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := New(config.Config{})
			service.verifier = verifierFunc(func(context.Context, string) (platformprincipal.Principal, error) {
				return test.principal, nil
			})
			got, _, err := service.VerifyWithState(t.Context(), "remote-token")
			if test.wantError {
				if err == nil || errors.Is(err, ErrStateUnavailable) {
					t.Fatalf("VerifyWithState() principal=%+v error=%v, want invalid claims", got, err)
				}
				return
			}
			if err != nil || got != test.principal {
				t.Fatalf("VerifyWithState() principal=%+v error=%v, want %+v", got, err, test.principal)
			}
		})
	}
}

func TestService_RemoteVerificationRespectsCallerDeadline(t *testing.T) {
	service := New(config.Config{Auth: config.Auth{JWKSTimeout: time.Second}})
	service.verifier = verifierFunc(func(ctx context.Context, _ string) (platformprincipal.Principal, error) {
		<-ctx.Done()
		return platformprincipal.Principal{}, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := service.Verify(ctx, "remote-token")
	if !errors.Is(err, ErrStateUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Verify error = %v, want caller deadline", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("verification ignored the shorter caller deadline")
	}
}

func TestService_IssueRejectsEmptyPrincipal(t *testing.T) {
	t.Parallel()
	jwtConfig, err := testutil.JWTConfig()
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{JWT: jwtConfig})
	if _, err := service.IssuePrincipal(platformprincipal.Principal{Type: platformprincipal.TypeUser}); err == nil {
		t.Fatal("IssuePrincipal() accepted an empty subject")
	}
}

func TestService_IssueRejectsUnsafeAccessTokenLifetimeWithoutLoader(t *testing.T) {
	t.Parallel()
	jwtConfig, err := testutil.JWTConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		ttl  time.Duration
	}{
		{name: "zero", ttl: 0},
		{name: "too short", ttl: 59 * time.Second},
		{name: "too long", ttl: 25 * time.Hour},
	} {
		t.Run(test.name, func(t *testing.T) {
			jwtConfig.TTL = test.ttl
			service := New(config.Config{JWT: jwtConfig})
			if _, err := service.Issue("service-account-1"); err == nil {
				t.Fatalf("Issue() accepted unsafe ttl %s", test.ttl)
			}
		})
	}
}

func TestService_IssueRejectsUnsafePrincipalShapes(t *testing.T) {
	t.Parallel()
	jwtConfig, err := testutil.JWTConfig()
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{JWT: jwtConfig})
	for _, principal := range []platformprincipal.Principal{
		{ID: "user-1", Type: platformprincipal.TypeUser},
		{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1", TenantID: "tenant-1"},
		{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1", MembershipID: "member-1"},
		{ID: "service-1", Type: platformprincipal.TypeServiceAccount, SessionID: "session-1"},
		{ID: "service-1", Type: platformprincipal.TypeServiceAccount, TenantID: "tenant-1"},
		{ID: "service-1", Type: platformprincipal.TypeServiceAccount, MembershipID: "member-1"},
		{ID: "system-1", Type: platformprincipal.TypeSystem},
		{ID: "unknown-1", Type: platformprincipal.Type("unknown")},
	} {
		if _, err := service.IssuePrincipal(principal); err == nil {
			t.Fatalf("IssuePrincipal(%+v) succeeded", principal)
		}
	}
}

func TestService_RejectsUserOnlyStateOnServiceAccountToken(t *testing.T) {
	jwtConfig, err := testutil.JWTConfig()
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{JWT: jwtConfig})
	if _, err := service.IssuePrincipalWithState(platformprincipal.Principal{ID: "service-1", Type: platformprincipal.TypeServiceAccount}, TokenState{MustChangePassword: true}); err == nil {
		t.Fatal("IssuePrincipalWithState accepted user-only password state")
	}
	now := time.Now()
	for _, test := range []struct {
		name   string
		mutate func(*Claims)
	}{
		{name: "session", mutate: func(c *Claims) { c.SessionID = "session-1" }},
		{name: "tenant", mutate: func(c *Claims) { c.TenantID = "tenant-1" }},
		{name: "membership", mutate: func(c *Claims) { c.MembershipID = "member-1" }},
		{name: "password state", mutate: func(c *Claims) { c.MustChangePassword = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			claims := Claims{RegisteredClaims: jwtlib.RegisteredClaims{
				Issuer: jwtConfig.Issuer, Audience: jwtlib.ClaimStrings{jwtConfig.Audience}, Subject: "service-1", ID: "token-1",
				IssuedAt: jwtlib.NewNumericDate(now), NotBefore: jwtlib.NewNumericDate(now), ExpiresAt: jwtlib.NewNumericDate(now.Add(time.Hour)),
			}, PrincipalType: platformprincipal.TypeServiceAccount}
			test.mutate(&claims)
			token := jwtlib.NewWithClaims(jwtlib.GetSigningMethod(service.active.algorithm), claims)
			token.Header["kid"] = service.active.id
			raw, err := token.SignedString(service.active.private)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Parse(raw); err == nil {
				t.Fatal("Parse accepted user-only fields on service-account token")
			}
			if _, _, err := service.VerifyWithState(t.Context(), raw); err == nil || errors.Is(err, ErrStateUnavailable) {
				t.Fatalf("VerifyWithState error = %v, want invalid token without database lookup", err)
			}
		})
	}
}

func TestServiceVerifyRevalidatesTenantMembershipOwnership(t *testing.T) {
	t.Parallel()
	jwtConfig, err := testutil.JWTConfig()
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{JWT: jwtConfig})
	raw, err := service.IssuePrincipal(platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1", TenantID: "tenant-1", MembershipID: "member-1"})
	if err != nil {
		t.Fatal(err)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service.db = sqlx.NewDb(db, "sqlmock")
	mock.ExpectQuery(`SELECT c.must_change_password FROM identity_sessions s JOIN identity_users u.*EXISTS \(SELECT 1 FROM tenant_memberships m JOIN tenants t`).
		WithArgs("session-1", "user-1", sqlmock.AnyArg(), "member-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"must_change_password"}))

	if _, err := service.Verify(t.Context(), raw); err == nil {
		t.Fatal("Verify() accepted a tenant membership not owned by the token subject")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestService_VerifyDoesNotDelegateInvalidLocallySignedToken(t *testing.T) {
	t.Parallel()
	jwtConfig, err := testutil.JWTConfig()
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{JWT: jwtConfig})
	now := time.Now()
	claims := Claims{RegisteredClaims: jwtlib.RegisteredClaims{
		Issuer: jwtConfig.Issuer, Audience: jwtlib.ClaimStrings{jwtConfig.Audience}, Subject: "user-1", ID: "token-1",
		IssuedAt: jwtlib.NewNumericDate(now), NotBefore: jwtlib.NewNumericDate(now), ExpiresAt: jwtlib.NewNumericDate(now.Add(time.Hour)),
	}, PrincipalType: platformprincipal.TypeUser}
	token := jwtlib.NewWithClaims(jwtlib.GetSigningMethod(service.active.algorithm), claims)
	token.Header["kid"] = service.active.id
	raw, err := token.SignedString(service.active.private)
	if err != nil {
		t.Fatal(err)
	}
	// A zero verifier would panic if called. A token naming a local key must
	// remain on the local validation path even when remote JWKS is configured.
	service.verifier = new(authn.JWKSVerifier)
	if _, err := service.Verify(t.Context(), raw); err == nil {
		t.Fatal("Verify() accepted a user token without a session")
	}
}

func TestService_KeyRotationKeepsOldTokensVerifiableAndPublishesBothKeys(t *testing.T) {
	t.Parallel()
	oldJWT, err := testutil.JWTConfig()
	if err != nil {
		t.Fatal(err)
	}
	oldJWT.KeyID = "rsa-old"
	oldService := New(config.Config{JWT: oldJWT})
	oldToken, err := oldService.Issue("service-a")
	if err != nil {
		t.Fatal(err)
	}
	oldPrivate, err := jwtlib.ParseRSAPrivateKeyFromPEM([]byte(oldJWT.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	oldPublicDER, err := x509.MarshalPKIXPublicKey(&oldPrivate.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	oldPublicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: oldPublicDER})

	newJWT, err := testutil.JWTConfig()
	if err != nil {
		t.Fatal(err)
	}
	newJWT.KeyID = "rsa-new"
	newJWT.PublicKeys = []config.JWTVerificationKey{{KeyID: "rsa-old", Algorithm: "RS256", PublicKey: string(oldPublicPEM)}}
	rotated := New(config.Config{JWT: newJWT})
	claims, err := rotated.Parse(oldToken)
	if err != nil || claims.Subject != "service-a" {
		t.Fatalf("rotated Parse() claims=%+v err=%v", claims, err)
	}
	keys := rotated.JWKS().Keys
	if len(keys) != 2 || keys[0].KID != "rsa-new" && keys[1].KID != "rsa-new" {
		t.Fatalf("rotated JWKS=%+v", keys)
	}
}

func TestService_IssueAndParse(t *testing.T) {
	t.Parallel()
	jwtConfig, err := testutil.JWTConfig()
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{JWT: jwtConfig})
	token, err := service.Issue("client")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	claims, err := service.Parse(token)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if claims.Subject != "client" {
		t.Fatalf("Subject = %q, want client", claims.Subject)
	}
}

func TestService_IssuePrincipalWithStateRoundTripsPasswordRequirement(t *testing.T) {
	t.Parallel()
	jwtConfig, err := testutil.JWTConfig()
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{JWT: jwtConfig})
	raw, err := service.IssuePrincipalWithState(
		platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, SessionID: "session-1"},
		TokenState{MustChangePassword: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := service.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !claims.MustChangePassword {
		t.Fatal("must_change_password claim was not preserved")
	}
}
