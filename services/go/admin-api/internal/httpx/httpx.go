// Package httpx holds small shared HTTP helpers for admin-api: JSON
// responses, the {"error": ...} envelope (same shape as packages/go-auth),
// and a middleware requiring ANY of a set of Keycloak realm roles.
package httpx

import (
	"encoding/json"
	"net/http"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
)

// JSON writes v as an application/json response with the given status code.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes the standard H2Fleet error envelope: {"error": "<msg>"}.
func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}

// RequireAnyRole allows the request through when the authenticated principal
// carries at least one of the given realm roles. It must run AFTER
// jwtmw.RequireAuth so claims are present in the context.
func RequireAnyRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !auth.HasAnyRole(r.Context(), roles...) {
				Error(w, http.StatusForbidden, "missing required realm role (any of): "+joinRoles(roles))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func joinRoles(roles []string) string {
	out := ""
	for i, r := range roles {
		if i > 0 {
			out += ", "
		}
		out += r
	}
	return out
}
