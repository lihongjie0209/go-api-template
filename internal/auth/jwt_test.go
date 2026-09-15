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
	mock.ExpectQuery(`SELECT count\(\*\) FROM identity_sessions s JOIN identity_users u`).WithArgs("session-1", "user-1", sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	if _, err := service.Verify(context.Background(), raw); err == nil {
		t.Fatal("Verify() accepted a session for a disabled user")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
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
	service := New(config.Config{JWT: jwtConfig, Auth: config.Auth{ClientID: "client", ClientSecret: "secret"}})
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

func TestService_Authenticate(t *testing.T) {
	t.Parallel()
	jwtConfig, err := testutil.JWTConfig()
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{JWT: jwtConfig, Auth: config.Auth{ClientID: "client", ClientSecret: "secret"}})
	if !service.Authenticate("client", "secret") {
		t.Fatal("Authenticate() = false, want true")
	}
	if service.Authenticate("client", "wrong") {
		t.Fatal("Authenticate() = true, want false")
	}
}
