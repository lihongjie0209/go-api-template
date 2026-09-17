package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
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
	mock.ExpectQuery(`SELECT count\(\*\) FROM identity_sessions s JOIN identity_users u`).WithArgs("session-1", "user-1", sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	if _, err := service.Verify(context.Background(), raw); err == nil {
		t.Fatal("Verify() accepted a session for a disabled user")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
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
		{ID: "system-1", Type: platformprincipal.TypeSystem},
		{ID: "unknown-1", Type: platformprincipal.Type("unknown")},
	} {
		if _, err := service.IssuePrincipal(principal); err == nil {
			t.Fatalf("IssuePrincipal(%+v) succeeded", principal)
		}
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
	mock.ExpectQuery(`SELECT count\(\*\) FROM identity_sessions s JOIN identity_users u.*EXISTS \(SELECT 1 FROM tenant_memberships m JOIN tenants t`).
		WithArgs("session-1", "user-1", sqlmock.AnyArg(), "member-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

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
