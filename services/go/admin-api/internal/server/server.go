// Package server assembles the admin-api HTTP router so it can be exercised
// in tests with mock dependencies (mock JWKS, fake stores, stub upstreams).
package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/httpx"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/kpi"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/metrics"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/onboarding"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/ops"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/users"
)

// RoleGate notes:
//   - onboarding intake + citizen self-serve: public (no JWT)
//   - onboarding list/get/approve/reject: Keycloak role platform-admin OR operator
//   - user management: platform-admin only
//   - admin KPI/health/alerts/toggles feed: platform-admin OR operator ("operator+")
//   - toggle mutation (proxied to toggle-service): platform-admin only

// Deps are the router dependencies.
type Deps struct {
	Log        *zap.Logger
	JWT        *auth.Middleware
	Healthz    http.HandlerFunc
	Onboarding *onboarding.Handler
	Users      *users.Handler
	KPIs       *kpi.Aggregator
	Ops        *ops.Handler
}

// NewRouter builds the admin-api chi router.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer,
		metrics.Middleware("admin-api"), middleware.Timeout(30*time.Second))
	r.Get("/healthz", d.Healthz)
	r.Handle("/metrics", metrics.Handler())

	operatorOrAdmin := httpx.RequireAnyRole("operator", "platform-admin")

	// ---------------------------------------------------------- onboarding --
	r.Post("/v1/onboarding/citizen", d.Onboarding.CitizenSelfServe) // public self-serve
	r.Post("/v1/onboarding/{key}", d.Onboarding.Intake)             // public intake (pending)
	r.Group(func(r chi.Router) {
		r.Use(d.JWT.RequireAuth, operatorOrAdmin)
		r.Get("/v1/onboarding", d.Onboarding.List)
		r.Get("/v1/onboarding/{key}", d.Onboarding.Get)
		r.Post("/v1/onboarding/{key}/approve", d.Onboarding.Approve)
		r.Post("/v1/onboarding/{key}/reject", d.Onboarding.Reject)
	})

	// ----------------------------------------------------- user management --
	r.Group(func(r chi.Router) {
		r.Use(d.JWT.RequireRole("platform-admin"))
		r.Get("/v1/users", d.Users.List)
		r.Post("/v1/users", d.Users.Create)
		r.Put("/v1/users/{id}/roles", d.Users.UpdateRoles)
		r.Post("/v1/users/{id}/disable", d.Users.Disable)
		r.Post("/v1/users/{id}/enable", d.Users.Enable)
		r.Post("/v1/users/{id}/reset-password", d.Users.ResetPassword)
	})

	// --------------------------------------------------- admin ops surface --
	r.Group(func(r chi.Router) {
		r.Use(d.JWT.RequireAuth, operatorOrAdmin)
		r.Get("/v1/admin/kpis", kpiHandler(d.KPIs))
		r.Get("/v1/admin/health", d.Ops.Health)
		r.Get("/v1/admin/alerts", d.Ops.Alerts)
		r.Get("/v1/admin/toggles", d.Ops.ListToggles)
	})
	// toggle-service owns feature_toggles; the mutation is proxied with the
	// caller's JWT and additionally gated to platform-admin here.
	r.With(d.JWT.RequireRole("platform-admin")).Put("/v1/admin/toggles/{module}", d.Ops.UpdateToggle)

	return r
}

func kpiHandler(agg *kpi.Aggregator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, agg.Collect(r.Context()))
	}
}
