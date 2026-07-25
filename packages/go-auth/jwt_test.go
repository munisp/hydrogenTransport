package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

// jwksEnv is a mock Keycloak JWKS endpoint + signing key for middleware tests.
type jwksEnv struct {
	issuer string
	key    *rsa.PrivateKey
	srv    *httptest.Server
}

func newJWKS(t *testing.T) *jwksEnv {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	env := &jwksEnv{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/h2fleet/protocol/openid-connect/certs", func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}) // 65537
		fmt.Fprintf(w, `{"keys":[{"kty":"RSA","kid":"test","use":"sig","alg":"RS256","n":%q,"e":%q}]}`, n, e)
	})
	env.srv = httptest.NewServer(mux)
	t.Cleanup(env.srv.Close)
	env.issuer = env.srv.URL + "/realms/h2fleet"
	return env
}

func (e *jwksEnv) token(t *testing.T, sub string, roles ...string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":          e.issuer,
		"sub":          sub,
		"exp":          time.Now().Add(time.Hour).Unix(),
		"iat":          time.Now().Unix(),
		"realm_access": map[string]any{"roles": roles},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "test"
	signed, err := tok.SignedString(e.key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// RequireAnyRole backs the infra-api incident-list gate: staff roles pass,
// plain citizens get 403, missing tokens 401.
func TestRequireAnyRole(t *testing.T) {
	env := newJWKS(t)
	mw := New(env.issuer, zap.NewNop())
	gated := mw.RequireAnyRole("operator", "platform-admin", "station-staff")(okHandler())

	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"operator allowed", env.token(t, "op-1", "operator"), http.StatusOK},
		{"platform-admin allowed", env.token(t, "adm-1", "platform-admin"), http.StatusOK},
		{"station-staff allowed", env.token(t, "st-1", "station-staff"), http.StatusOK},
		{"citizen forbidden", env.token(t, "cit-1", "citizen"), http.StatusForbidden},
		{"driver forbidden", env.token(t, "drv-1", "driver"), http.StatusForbidden},
		{"no token", "", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/incidents", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			gated.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("got %d want %d (body: %s)", rec.Code, tc.want, rec.Body)
			}
		})
	}
}
