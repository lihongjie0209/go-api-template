package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/base64"
	"encoding/json"
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
	PrincipalType        platformprincipal.Type `json:"principal_type,omitempty"`
	SessionID            string                 `json:"session_id,omitempty"`
	TenantID             string                 `json:"tenant_id,omitempty"`
	MembershipID         string                 `json:"membership_id,omitempty"`
	MustChangePassword   bool                   `json:"must_change_password,omitempty"`
	CredentialGeneration int64                  `json:"credential_generation,omitempty"`
}

type TokenState struct {
	MustChangePassword bool
}

// ErrStateUnavailable means a signed token could not be checked against the
// authoritative session or account store. Callers must not treat it as revocation.
var ErrStateUnavailable = errors.New("authentication state unavailable")

const maxBearerTokenBytes = 16 * 1024

type signingKey struct {
	id, algorithm   string
	private, public any
}

type bearerVerifier interface {
	VerifyBearer(context.Context, string) (platformprincipal.Principal, error)
	Close()
}

type Service struct {
	issuer, audience string
	ttl              time.Duration
	remoteTimeout    time.Duration
	active           *signingKey
	verification     map[string]signingKey
	verifier         bearerVerifier
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
	verifier, err := authn.NewJWKSVerifier(context.Background(), authn.JWKSConfig{URL: cfg.Auth.JWKSURL, Issuer: cfg.Auth.Issuer, Audience: cfg.Auth.Audience, HTTPTimeout: service.remoteTimeout})
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
	remoteTimeout := cfg.Auth.JWKSTimeout
	if remoteTimeout <= 0 {
		remoteTimeout = 2 * time.Second
	}
	s := &Service{issuer: cfg.JWT.Issuer, audience: cfg.JWT.Audience, ttl: cfg.JWT.TTL, remoteTimeout: remoteTimeout, verification: map[string]signingKey{}, db: db}
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
	if err := ctx.Err(); err != nil {
		return platformprincipal.Principal{}, TokenState{}, fmt.Errorf("%w: verify token: %w", ErrStateUnavailable, err)
	}
	if len(raw) == 0 || len(raw) > maxBearerTokenBytes {
		return platformprincipal.Principal{}, TokenState{}, errors.New("invalid bearer token length")
	}
	if len(s.verification) == 0 {
		if s.verifier == nil {
			return platformprincipal.Principal{}, TokenState{}, errors.New("jwt verification keys are not configured")
		}
		return s.verifyRemoteWithState(ctx, raw)
	}
	claims, parseErr := s.Parse(raw)
	if parseErr != nil {
		if s.verifier != nil && !s.referencesLocalKey(raw) {
			return s.verifyRemoteWithState(ctx, raw)
		}
		return platformprincipal.Principal{}, TokenState{}, parseErr
	}
	principal := platformprincipal.Principal{ID: claims.Subject, Type: claims.PrincipalType, SessionID: claims.SessionID, TenantID: claims.TenantID, MembershipID: claims.MembershipID}
	state, err := s.verifyPrincipalState(ctx, principal, claims.CredentialGeneration)
	if err != nil {
		return platformprincipal.Principal{}, TokenState{}, err
	}
	return principal, state, nil
}

func (s *Service) verifyRemoteWithState(ctx context.Context, raw string) (platformprincipal.Principal, TokenState, error) {
	principal, err := s.verifyRemote(ctx, raw)
	if err != nil {
		return platformprincipal.Principal{}, TokenState{}, err
	}
	var generation int64
	if principal.Type == platformprincipal.TypeServiceAccount {
		// The shared verifier authenticated this token but its Principal contains
		// no application-specific claims. Inspect only that verified token.
		generation, err = serviceAccountGeneration(raw)
		if err != nil {
			return platformprincipal.Principal{}, TokenState{}, err
		}
	}
	state, err := s.verifyPrincipalState(ctx, principal, generation)
	if err != nil {
		return platformprincipal.Principal{}, TokenState{}, err
	}
	return principal, state, nil
}

// verifyPrincipalState is the authoritative mutable-state boundary for both
// locally signed and remotely verified bearer principals.
func (s *Service) verifyPrincipalState(ctx context.Context, principal platformprincipal.Principal, generation int64) (TokenState, error) {
	if s.db == nil {
		return TokenState{}, ErrStateUnavailable
	}
	var state TokenState
	switch principal.Type {
	case platformprincipal.TypeUser:
		query := `SELECT c.must_change_password ` + activeUserSessionSource
		args := []any{principal.SessionID, principal.ID, time.Now()}
		if principal.TenantID != "" {
			query += ` AND EXISTS (SELECT 1 FROM tenant_memberships m JOIN tenants t ON t.id=m.tenant_id AND t.status='active' AND t.deleted_at IS NULL WHERE m.id=? AND m.tenant_id=? AND m.user_id=s.user_id AND m.status='active' AND m.deleted_at IS NULL)`
			args = append(args, principal.MembershipID, principal.TenantID)
		}
		if err := s.db.GetContext(ctx, &state.MustChangePassword, s.db.Rebind(query), args...); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return TokenState{}, fmt.Errorf("%w: verify token: %w", ErrStateUnavailable, contextErr)
			}
			if errors.Is(err, sql.ErrNoRows) {
				return TokenState{}, errors.New("session is revoked or expired")
			}
			return TokenState{}, fmt.Errorf("%w: %w", ErrStateUnavailable, err)
		}
	case platformprincipal.TypeServiceAccount:
		if generation <= 0 {
			return TokenState{}, errors.New("service account token credential generation is required")
		}
		var currentGeneration int64
		query := s.db.Rebind(`SELECT credential_generation FROM identity_service_accounts WHERE id=? AND status='active' AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at>?)`)
		if err := s.db.GetContext(ctx, &currentGeneration, query, principal.ID, time.Now()); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return TokenState{}, fmt.Errorf("%w: verify token: %w", ErrStateUnavailable, contextErr)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return TokenState{}, fmt.Errorf("%w: %w", ErrStateUnavailable, err)
			}
			return TokenState{}, errors.New("service account is disabled or expired")
		}
		if currentGeneration != generation {
			return TokenState{}, errors.New("service account credential has been rotated")
		}
	default:
		return TokenState{}, errors.New("unsupported principal type")
	}
	if err := ctx.Err(); err != nil {
		return TokenState{}, fmt.Errorf("%w: verify token: %w", ErrStateUnavailable, err)
	}
	return state, nil
}

func (s *Service) verifyRemote(ctx context.Context, raw string) (platformprincipal.Principal, error) {
	verificationCtx, cancel := context.WithTimeout(ctx, s.remoteTimeout)
	defer cancel()
	identity, err := s.verifier.VerifyBearer(verificationCtx, raw)
	if deadlineErr := verificationCtx.Err(); deadlineErr != nil {
		return platformprincipal.Principal{}, fmt.Errorf("%w: remote token verification: %w", ErrStateUnavailable, deadlineErr)
	}
	if errors.Is(err, authn.ErrJWKSUnavailable) {
		return platformprincipal.Principal{}, fmt.Errorf("%w: remote token verification: %w", ErrStateUnavailable, err)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return platformprincipal.Principal{}, fmt.Errorf("%w: remote token verification: %w", ErrStateUnavailable, err)
	}
	if err != nil {
		return platformprincipal.Principal{}, err
	}
	if strings.TrimSpace(identity.ID) == "" {
		return platformprincipal.Principal{}, errors.New("remote jwt subject is required")
	}
	switch identity.Type {
	case platformprincipal.TypeUser:
		if strings.TrimSpace(identity.SessionID) == "" {
			return platformprincipal.Principal{}, errors.New("remote user jwt requires a session id")
		}
		if (strings.TrimSpace(identity.TenantID) == "") != (strings.TrimSpace(identity.MembershipID) == "") {
			return platformprincipal.Principal{}, errors.New("remote user jwt tenant and membership context must be paired")
		}
	case platformprincipal.TypeServiceAccount:
		if identity.SessionID != "" || identity.TenantID != "" || identity.MembershipID != "" {
			return platformprincipal.Principal{}, errors.New("service account jwt cannot carry user session or tenant context")
		}
	default:
		return platformprincipal.Principal{}, errors.New("invalid remote jwt principal type")
	}
	return identity, nil
}
func (s *Service) Enabled() bool { return s.active != nil && s.initErr == nil }
func (s *Service) Issue(subject string) (string, error) {
	return s.IssueServiceAccount(subject, 1)
}
func (s *Service) IssueServiceAccount(subject string, generation int64) (string, error) {
	return s.issuePrincipalWithGeneration(platformprincipal.Principal{ID: subject, Type: platformprincipal.TypeServiceAccount}, TokenState{}, generation)
}
func (s *Service) IssuePrincipal(principal platformprincipal.Principal) (string, error) {
	return s.IssuePrincipalWithState(principal, TokenState{})
}

func (s *Service) IssuePrincipalWithState(principal platformprincipal.Principal, state TokenState) (string, error) {
	generation := int64(0)
	if principal.Type == platformprincipal.TypeServiceAccount {
		generation = 1
	}
	return s.issuePrincipalWithGeneration(principal, state, generation)
}

func (s *Service) issuePrincipalWithGeneration(principal platformprincipal.Principal, state TokenState, generation int64) (string, error) {
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
	if principal.Type == platformprincipal.TypeServiceAccount && (principal.SessionID != "" || principal.TenantID != "" || principal.MembershipID != "" || state.MustChangePassword) {
		return "", errors.New("service account jwt cannot carry user session or tenant context")
	}
	if principal.Type == platformprincipal.TypeServiceAccount && generation <= 0 {
		return "", errors.New("service account credential generation must be positive")
	}
	if s.ttl < config.MinAccessTokenTTL || s.ttl > config.MaxAccessTokenTTL {
		return "", errors.New("jwt.ttl must be between 1 minute and 24 hours")
	}
	now := time.Now()
	jti, err := randomID()
	if err != nil {
		return "", fmt.Errorf("create token id: %w", err)
	}
	claims := Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: s.issuer, Audience: jwt.ClaimStrings{s.audience}, Subject: principal.ID, ID: jti, IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl))}, PrincipalType: principal.Type, SessionID: principal.SessionID, TenantID: principal.TenantID, MembershipID: principal.MembershipID, MustChangePassword: state.MustChangePassword, CredentialGeneration: generation}
	token := jwt.NewWithClaims(jwt.GetSigningMethod(s.active.algorithm), claims)
	token.Header["kid"] = s.active.id
	encoded, err := token.SignedString(s.active.private)
	if err != nil {
		return "", err
	}
	if len(encoded) > maxBearerTokenBytes {
		return "", errors.New("issued bearer token exceeds maximum length")
	}
	return encoded, nil
}
func (s *Service) Parse(raw string) (*Claims, error) {
	if len(raw) == 0 || len(raw) > maxBearerTokenBytes {
		return nil, errors.New("invalid bearer token length")
	}
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
	if claims.PrincipalType == platformprincipal.TypeServiceAccount && (claims.SessionID != "" || claims.TenantID != "" || claims.MembershipID != "" || claims.MustChangePassword) {
		return nil, errors.New("service account jwt cannot carry user session or tenant context")
	}
	if claims.PrincipalType == platformprincipal.TypeServiceAccount && claims.CredentialGeneration <= 0 {
		return nil, errors.New("service account credential generation is required")
	}
	if claims.PrincipalType == platformprincipal.TypeServiceAccount {
		generation, err := serviceAccountGeneration(raw)
		if err != nil || generation != claims.CredentialGeneration {
			return nil, errors.New("invalid service account token claims")
		}
	}
	if claims.PrincipalType == platformprincipal.TypeUser && claims.CredentialGeneration != 0 {
		return nil, errors.New("user jwt cannot carry service account credential generation")
	}
	return claims, nil
}

// serviceAccountGeneration reads extra claims only after the caller has
// verified the signature. A present password-change claim is forbidden even
// when its JSON value is false or null.
func serviceAccountGeneration(raw string) (int64, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return 0, errors.New("invalid service account token claims")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, errors.New("invalid service account token claims")
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(payload, &claims); err != nil {
		return 0, errors.New("invalid service account token claims")
	}
	for key := range claims {
		if strings.EqualFold(key, "must_change_password") {
			return 0, errors.New("service account token cannot carry password-change state")
		}
	}
	var generation int64
	if err := json.Unmarshal(claims["credential_generation"], &generation); err != nil || generation <= 0 {
		return 0, errors.New("service account credential generation is required")
	}
	return generation, nil
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
		encoded, err := public.Bytes()
		if err != nil || len(encoded) != 65 || encoded[0] != 4 {
			return JWK{}
		}
		return JWK{KTY: "EC", Use: "sig", KID: key.id, ALG: key.algorithm, CRV: "P-256", X: base64.RawURLEncoding.EncodeToString(encoded[1:33]), Y: base64.RawURLEncoding.EncodeToString(encoded[33:])}
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
