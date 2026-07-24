// Package auth implements Keycloak OIDC JWT (RS256) middleware (SPEC §3.5).
// Mutating routes require a valid Bearer token; admin routes additionally
// require a Keycloak realm role (e.g. platform-admin).
//
// Issuer handling: browser/PWA tokens are issued with the public issuer URL
// (e.g. http://localhost:8088/realms/h2fleet) while in-network services reach
// Keycloak via an internal URL (e.g. http://keycloak:8080/realms/h2fleet).
// The middleware therefore accepts a set of valid issuers: KEYCLOAK_ISSUER
// (also used for JWKS fetching) plus the optional comma-separated
// KEYCLOAK_ISSUER_ALT env (default http://localhost:8088/realms/h2fleet).
package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

// defaultAltIssuer is the public (browser-facing) Keycloak issuer accepted in
// addition to the in-network KEYCLOAK_ISSUER.
const defaultAltIssuer = "http://localhost:8088/realms/h2fleet"

type contextKey string

// ClaimsKey is the context key under which validated JWT claims are stored.
const ClaimsKey contextKey = "h2fleet.claims"

// ClaimsFromContext returns the validated JWT claims, or nil.
func ClaimsFromContext(ctx context.Context) jwt.MapClaims {
	c, _ := ctx.Value(ClaimsKey).(jwt.MapClaims)
	return c
}

// Subject returns the `sub` claim of the authenticated principal ("" if none).
func Subject(ctx context.Context) string {
	if c := ClaimsFromContext(ctx); c != nil {
		if sub, ok := c["sub"].(string); ok {
			return sub
		}
	}
	return ""
}

type jwksKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwksDocument struct {
	Keys []jwksKey `json:"keys"`
}

// Middleware validates RS256 JWTs issued by a Keycloak realm. Public keys are
// fetched from the in-network realm JWKS endpoint (KEYCLOAK_ISSUER) and cached
// (refresh every 5 minutes or on unknown kid). The `iss` claim is validated
// against any of the accepted issuers (in-network + browser-facing aliases).
type Middleware struct {
	issuers []string // accepted iss claim values
	jwksURL string   // in-network JWKS endpoint (derived from KEYCLOAK_ISSUER only)
	log     *zap.Logger
	http    *http.Client

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

// New builds the middleware. issuer is the in-network Keycloak realm issuer
// URL (KEYCLOAK_ISSUER), e.g. http://keycloak:8080/realms/h2fleet. Additional
// accepted issuers come from KEYCLOAK_ISSUER_ALT (comma-separated; defaults to
// http://localhost:8088/realms/h2fleet). When issuer is empty, all guarded
// routes fail closed with 503 (auth not configured).
func New(issuer string, log *zap.Logger) *Middleware {
	issuer = strings.TrimSuffix(issuer, "/")

	issuers := []string{}
	if issuer != "" {
		issuers = append(issuers, issuer)
	}
	alt := os.Getenv("KEYCLOAK_ISSUER_ALT")
	if alt == "" {
		alt = defaultAltIssuer
	}
	for _, a := range strings.Split(alt, ",") {
		a = strings.TrimSuffix(strings.TrimSpace(a), "/")
		if a == "" {
			continue
		}
		dup := false
		for _, existing := range issuers {
			if existing == a {
				dup = true
				break
			}
		}
		if !dup {
			issuers = append(issuers, a)
		}
	}

	if issuer == "" {
		log.Warn("KEYCLOAK_ISSUER not set; JWT-protected routes will reject requests")
	} else {
		log.Info("jwt middleware configured", zap.Strings("accepted_issuers", issuers))
	}

	jwksURL := ""
	if issuer != "" {
		jwksURL = issuer + "/protocol/openid-connect/certs"
	}
	return &Middleware{
		issuers: issuers,
		jwksURL: jwksURL,
		log:     log,
		http:    &http.Client{Timeout: 5 * time.Second},
		keys:    map[string]*rsa.PublicKey{},
	}
}

// RequireAuth rejects requests without a valid Keycloak JWT.
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return m.guard(next)
}

// RequireRole rejects requests without a valid JWT carrying the given realm role.
func (m *Middleware) RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return m.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !hasRole(ClaimsFromContext(r.Context()), role) {
				writeError(w, http.StatusForbidden, "missing required realm role: "+role)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

func (m *Middleware) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.jwksURL == "" {
			writeError(w, http.StatusServiceUnavailable, "authentication not configured (KEYCLOAK_ISSUER unset)")
			return
		}
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		claims, err := m.verify(r.Context(), token)
		if err != nil {
			m.log.Debug("jwt verification failed", zap.Error(err))
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ClaimsKey, claims)))
	})
}

func (m *Middleware) verify(ctx context.Context, raw string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		kid, _ := t.Header["kid"].(string)
		key, err := m.publicKey(ctx, kid)
		if err != nil {
			return nil, err
		}
		return key, nil
	},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, err
	}
	iss, _ := claims["iss"].(string)
	if !m.acceptsIssuer(iss) {
		return nil, fmt.Errorf("unexpected issuer %q", iss)
	}
	return claims, nil
}

func (m *Middleware) acceptsIssuer(iss string) bool {
	iss = strings.TrimSuffix(iss, "/")
	for _, accepted := range m.issuers {
		if iss == accepted {
			return true
		}
	}
	return false
}

func (m *Middleware) publicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	m.mu.RLock()
	key, ok := m.keys[kid]
	stale := time.Since(m.fetchedAt) > 5*time.Minute
	m.mu.RUnlock()
	if ok && !stale {
		return key, nil
	}
	if err := m.refreshJWKS(ctx); err != nil {
		if ok { // serve stale key rather than failing on transient JWKS outage
			return key, nil
		}
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if key, ok := m.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("unknown kid %q", kid)
}

func (m *Middleware) refreshJWKS(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Since(m.fetchedAt) < 10*time.Second { // single-flight-ish
		return nil
	}
	resp, err := m.http.Get(m.jwksURL)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch jwks: status %d", resp.StatusCode)
	}
	var doc jwksDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("decode jwks: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := parseRSAPublicKey(k.N, k.E)
		if err != nil {
			m.log.Warn("skipping unparsable jwks key", zap.String("kid", k.Kid), zap.Error(err))
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return errors.New("jwks document contained no usable RSA keys")
	}
	m.keys = keys
	m.fetchedAt = time.Now()
	return nil
}

func parseRSAPublicKey(nB64, eB64 string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, err
	}
	n := new(big.Int).SetBytes(nBytes)
	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	if e == 0 {
		return nil, errors.New("invalid exponent")
	}
	return &rsa.PublicKey{N: n, E: e}, nil
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

func hasRole(claims jwt.MapClaims, role string) bool {
	if claims == nil {
		return false
	}
	realm, ok := claims["realm_access"].(map[string]any)
	if !ok {
		return false
	}
	roles, ok := realm["roles"].([]any)
	if !ok {
		return false
	}
	for _, r := range roles {
		if s, ok := r.(string); ok && s == role {
			return true
		}
	}
	return false
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
