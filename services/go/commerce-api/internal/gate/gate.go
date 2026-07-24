// Package gate gates route groups behind feature-toggle modules (SPEC §3.2):
// when a module is disabled its routes return 404.
package gate

import (
	"encoding/json"
	"net/http"

	toggle "github.com/munisp/hydrogenTransport/packages/toggle-client/go"
)

// Module returns middleware that serves 404 while the given module toggle is
// disabled. The toggle client is fail-closed, so a toggle-service outage also
// disables the module (SPEC §3.2: fail-open=false).
func Module(tc *toggle.Client, module string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !tc.IsEnabled(r.Context(), module) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "module disabled or unknown: " + module,
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
