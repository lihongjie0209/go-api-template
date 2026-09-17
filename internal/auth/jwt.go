package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/microservice-platform-go/authn"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"go.uber.org/fx"
)

type Claims struct {
	jwt.RegisteredClaims
	PrincipalType      platformprincipal.Type `json:"principal_type,omitempty"`
	SessionID          string                 `json:"session_id,omitempty"`
	TenantID           string                 `json:"tenant_id,omitempty"`
	MembershipID       string                 `json:"membership_id,omitempty"`
	MustChangePassword bool                   `json:"must_change_password,omitempty"`
}

type TokenState struct {
	MustChangePassword bool
}
type signingKey struct {
	id, algorithm   string
	private, public any
}
type Service struct {
	issuer, audience string
	ttl              time.Duration
	active           *signingKey
	verification     map[string]signingKey
	verifier         *authn.JWKSVerifier
	initErr          error
	db               *sqlx.DB
}
type JWK struct {
	KTY string `json:"kty"`
	Use string `json:"use"`
	KID string `json:"kid"`
	ALG string `json:"alg"`
	N   string `json:"n,omitempty"`
	E   string `json:"e,omitempty"`
	CRV string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
}
type JWKS struct {
	Keys []JWK `json:"keys"`
}

func NewRuntime(lifecycle fx.Lifecycle, cfg config.Config, db *sqlx.DB) (*Service, error) {
	service := NewWithDatabase(cfg, db)
	if service.initErr != nil {
		return nil, service.initErr
	}
	if cfg.Auth.JWKSURL == "" {
		return service, nil
	}
	verifier, err := authn.NewJWKSVerifier(context.Background(), authn.JWKSConfig{URL: cfg.Auth.JWKSURL, Issuer: cfg.Auth.Issuer, Audience: cfg.Auth.Audience})
	if err != nil {
		return nil, fmt.Errorf("configure identity token verifier: %w", err)
	}
	service.verifier = verifier
	lifecycle.Append(fx.StopHook(func() { verifier.Close() }))
	return service, nil
}
func New(cfg config.Config) *Service {
	return NewWithDatabase(cfg, nil)
}

func NewWithDatabase(cfg config.Config, db *sqlx.DB) *Service {
	s := &Service{issuer: cfg.JWT.Issuer, audience: cfg.JWT.Audience, ttl: cfg.JWT.TTL, verification: map[string]signingKey{}, db: db}
	if cfg.JWT.KeyID == "" || (cfg.JWT.PrivateKey == "" && cfg.JWT.PrivateKeyFile == "") {
		return s
	}
	pem, err := keyMaterial(cfg.JWT.PrivateKey, cfg.JWT.PrivateKeyFile)
	if err != nil {
		s.initErr = err
		return s
	}
	active, err := parsePrivateKey(cfg.JWT.KeyID, cfg.JWT.Algorithm, pem)
	if err != nil {
		s.initErr = err
		return s
	}
	s.active = &active
	s.verification[active.id] = active
	for _, configured := range cfg.JWT.PublicKeys {
		material, readErr := keyMaterial(configured.PublicKey, configured.PublicKeyFile)
		if readErr != nil {
			s.initErr = readErr
			return s
		}
		key, parseErr := parsePublicKey(configured.KeyID, configured.Algorithm, material)
		if parseErr != nil {
			s.initErr = parseErr
			return s
		}
		if _, exists := s.verification[key.id]; exists {
			s.initErr = fmt.Errorf("duplicate jwt key id %q", key.id)
			return s
		}
		s.verification[key.id] = key
	}
	return s
}
func (s *Service) Verify(ctx context.Context, raw string) (platformprincipal.Principal, error) {
	principal, _, err := s.VerifyWithState(ctx, raw)
	return principal, err
}

func (s *Service) VerifyWithState(ctx context.Context, raw string) (platformprincipal.Principal, TokenState, error) {
	if len(s.verification) == 0 {
		if s.verifier == nil {
			return platformprincipal.Principal{}, TokenState{}, errors.New("jwt verification keys are not configured")
		}
		principal, err := s.verifier.VerifyBearer(ctx, raw)
		return principal, TokenState{}, err
	}
	claims, parseErr := s.Parse(raw)
	if parseErr != nil {
		if s.verifier != nil && !s.referencesLocalKey(raw) {
			principal, err := s.verifier.VerifyBearer(ctx, raw)
			return principal, TokenState{}, err
		}
		return platformprincipal.Principal{}, TokenState{}, parseErr
	}
	t := claims.PrincipalType
	switch t {
	case platformprincipal.TypeUser:
		if s.db == nil {
			return platformprincipal.Principal{}, TokenState{}, errors.New("session validation is unavailable")
		}
		var count int
		query := `SELECT count(*) FROM identity_sessions s JOIN identity_users u ON u.id=s.user_id AND u.status='active' AND u.deleted_at IS NULL WHERE s.id=? AND s.user_id=? AND s.revoked_at IS NULL AND s.expires_at>? AND s.deleted_at IS NULL`
		args := []any{claims.SessionID, claims.Subject, time.Now()}
		if claims.TenantID != "" {
			query += ` AND EXISTS (SELECT 1 FROM tenant_memberships m JOIN tenants t ON t.id=m.tenant_id AND t.status='active' AND t.deleted_at IS NULL WHERE m.id=? AND m.tenant_id=? AND m.user_id=s.user_id AND m.status='active' AND m.deleted_at IS NULL)`
			args = append(args, claims.MembershipID, claims.TenantID)
		}
		if err := s.db.GetContext(ctx, &count, s.db.Rebind(query), args...); err != nil || count != 1 {
			return platformprincipal.Principal{}, TokenState{}, errors.New("session is revoked or expired")
		}
	case platformprincipal.TypeServiceAccount:
		if s.db == nil {
			return platformprincipal.Principal{}, TokenState{}, errors.New("service account validation is unavailable")
		}
		var count int
		query := s.db.Rebind(`SELECT count(*) FROM identity_service_accounts WHERE id=? AND status='active' AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at>?)`)
		if err := s.db.GetContext(ctx, &count, query, claims.Subject, time.Now()); err != nil || count != 1 {
			return platformprincipal.Principal{}, TokenState{}, errors.New("service account is disabled or expired")
		}
	}
	return platformprincipal.Principal{ID: claims.Subject, Type: t, SessionID: claims.SessionID, TenantID: claims.TenantID, MembershipID: claims.MembershipID}, TokenState{MustChangePassword: claims.MustChangePassword}, nil
}
func (s *Service) Enabled() bool { return s.active != nil && s.initErr == nil }
func (s *Service) Issue(subject string) (string, error) {
	return s.IssuePrincipal(platformprincipal.Principal{ID: subject, Type: platformprincipal.TypeServiceAccount})
}
func (s *Service) IssuePrincipal(principal platformprincipal.Principal) (string, error) {
	return s.IssuePrincipalWithState(principal, TokenState{})
}

func (s *Service) IssuePrincipalWithState(principal platformprincipal.Principal, state TokenState) (string, error) {
	if !s.Enabled() {
		return "", errors.New("asymmetric jwt signing is not configured")
	}
	if strings.TrimSpace(principal.ID) == "" || principal.Type == "" {
		return "", errors.New("jwt principal id and type are required")
	}
	if principal.Type != platformprincipal.TypeUser && principal.Type != platformprincipal.TypeServiceAccount {
		return "", errors.New("jwt principal type is not externally issuable")
	}
	if principal.Type == platformprincipal.TypeUser && strings.TrimSpace(principal.SessionID) == "" {
		return "", errors.New("user jwt requires a session id")
	}
	if principal.Type == platformprincipal.TypeUser && (strings.TrimSpace(principal.TenantID) == "") != (strings.TrimSpace(principal.MembershipID) == "") {
		return "", errors.New("user jwt tenant and membership context must be paired")
	}
	now := time.Now()
	jti, err := randomID()
	if err != nil {
		return "", fmt.Errorf("create token id: %w", err)
	}
	claims := Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: s.issuer, Audience: jwt.ClaimStrings{s.audience}, Subject: principal.ID, ID: jti, IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl))}, PrincipalType: principal.Type, SessionID: principal.SessionID, TenantID: principal.TenantID, MembershipID: principal.MembershipID, MustChangePassword: state.MustChangePassword}
	token := jwt.NewWithClaims(jwt.GetSigningMethod(s.active.algorithm), claims)
	token.Header["kid"] = s.active.id
	return token.SignedString(s.active.private)
}
func (s *Service) Parse(raw string) (*Claims, error) {
	if len(s.verification) == 0 || s.initErr != nil {
		return nil, errors.New("jwt verification keys are not configured")
	}
	options := []jwt.ParserOption{jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithIssuer(s.issuer), jwt.WithValidMethods([]string{"RS256", "ES256"})}
	if s.audience != "" {
		options = append(options, jwt.WithAudience(s.audience))
	}
	token, err := jwt.ParseWithClaims(raw, &Claims{}, func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, errors.New("jwt kid is required")
		}
		key, ok := s.verification[kid]
		if !ok || token.Method.Alg() != key.algorithm {
			return nil, errors.New("unknown jwt signing key")
		}
		return key.public, nil
	}, options...)
	if err != nil {
		return nil, fmt.Errorf("parse jwt: %w", err)
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid jwt claims")
	}
	if claims.PrincipalType != platformprincipal.TypeUser && claims.PrincipalType != platformprincipal.TypeServiceAccount {
		return nil, errors.New("invalid jwt principal type")
	}
	if claims.PrincipalType == platformprincipal.TypeUser && strings.TrimSpace(claims.SessionID) == "" {
		return nil, errors.New("user jwt requires a session id")
	}
	if claims.PrincipalType == platformprincipal.TypeUser && (strings.TrimSpace(claims.TenantID) == "") != (strings.TrimSpace(claims.MembershipID) == "") {
		return nil, errors.New("user jwt tenant and membership context must be paired")
	}
	return claims, nil
}

func (s *Service) referencesLocalKey(raw string) bool {
	token, _, err := jwt.NewParser().ParseUnverified(raw, &Claims{})
	if err != nil {
		return false
	}
	kid, _ := token.Header["kid"].(string)
	_, ok := s.verification[kid]
	return ok
}
func (s *Service) JWKS() JWKS {
	ids := make([]string, 0, len(s.verification))
	for id := range s.verification {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := JWKS{Keys: make([]JWK, 0, len(ids))}
	for _, id := range ids {
		result.Keys = append(result.Keys, publicJWK(s.verification[id]))
	}
	return result
}
func parsePrivateKey(id, algorithm string, pem []byte) (signingKey, error) {
	if id == "" {
		return signingKey{}, errors.New("jwt key id is required")
	}
	switch algorithm {
	case "RS256":
		private, err := jwt.ParseRSAPrivateKeyFromPEM(pem)
		if err != nil || private.N.BitLen() < 2048 {
			return signingKey{}, errors.New("RS256 requires an RSA private key of at least 2048 bits")
		}
		return signingKey{id: id, algorithm: algorithm, private: private, public: &private.PublicKey}, nil
	case "ES256":
		private, err := jwt.ParseECPrivateKeyFromPEM(pem)
		if err != nil || private.Curve != elliptic.P256() {
			return signingKey{}, errors.New("ES256 requires a P-256 private key")
		}
		return signingKey{id: id, algorithm: algorithm, private: private, public: &private.PublicKey}, nil
	default:
		return signingKey{}, errors.New("jwt algorithm must be RS256 or ES256")
	}
}
func parsePublicKey(id, algorithm string, pem []byte) (signingKey, error) {
	if id == "" {
		return signingKey{}, errors.New("jwt key id is required")
	}
	switch algorithm {
	case "RS256":
		public, err := jwt.ParseRSAPublicKeyFromPEM(pem)
		if err != nil || public.N.BitLen() < 2048 {
			return signingKey{}, errors.New("invalid RSA verification key")
		}
		return signingKey{id: id, algorithm: algorithm, public: public}, nil
	case "ES256":
		public, err := jwt.ParseECPublicKeyFromPEM(pem)
		if err != nil || public.Curve != elliptic.P256() {
			return signingKey{}, errors.New("invalid ECDSA verification key")
		}
		return signingKey{id: id, algorithm: algorithm, public: public}, nil
	default:
		return signingKey{}, errors.New("jwt algorithm must be RS256 or ES256")
	}
}
func keyMaterial(inline, file string) ([]byte, error) {
	if inline != "" {
		return []byte(inline), nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read jwt key: %w", err)
	}
	return data, nil
}
func publicJWK(key signingKey) JWK {
	switch public := key.public.(type) {
	case *rsa.PublicKey:
		return JWK{KTY: "RSA", Use: "sig", KID: key.id, ALG: key.algorithm, N: base64.RawURLEncoding.EncodeToString(public.N.Bytes()), E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(public.E)).Bytes())}
	case *ecdsa.PublicKey:
		return JWK{KTY: "EC", Use: "sig", KID: key.id, ALG: key.algorithm, CRV: "P-256", X: base64.RawURLEncoding.EncodeToString(public.X.FillBytes(make([]byte, 32))), Y: base64.RawURLEncoding.EncodeToString(public.Y.FillBytes(make([]byte, 32)))}
	default:
		return JWK{}
	}
}
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
