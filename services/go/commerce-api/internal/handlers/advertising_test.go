package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"go.uber.org/zap"
)

func campaignRow(id, name, status string, budget int64) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "name", "advertiser", "budget_minor", "status", "starts_at", "ends_at", "created_at",
	}).AddRow(id, name, "acme", budget, status, nil, nil,
		time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC))
}

func campaignRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	return withClaims(httptest.NewRequest(http.MethodPost, "/v1/ads/campaigns",
		strings.NewReader(body)), "ops-1", "operator")
}

// --- create validation (audit: no validation at API level) ------------------

func TestCreateCampaign_NegativeBudgetRejected(t *testing.T) {
	h := &Handler{log: zap.NewExample()} // no db: must not be reached
	rec := httptest.NewRecorder()
	h.CreateCampaign(rec, campaignRequest(t, `{"name":"summer","budget_minor":-500}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "budget") {
		t.Fatalf("error body should name budget: %s", rec.Body)
	}
}

func TestCreateCampaign_EndsBeforeStartsRejected(t *testing.T) {
	h := &Handler{log: zap.NewExample()}
	rec := httptest.NewRecorder()
	h.CreateCampaign(rec, campaignRequest(t,
		`{"name":"summer","budget_minor":100,"starts_at":"2026-08-01T00:00:00Z","ends_at":"2026-07-01T00:00:00Z"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "ends_at") {
		t.Fatalf("error body should name ends_at: %s", rec.Body)
	}
}

func TestCreateCampaign_BlankNameRejected(t *testing.T) {
	h := &Handler{log: zap.NewExample()}
	rec := httptest.NewRecorder()
	h.CreateCampaign(rec, campaignRequest(t, `{"name":"   ","budget_minor":0}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (body: %s)", rec.Code, rec.Body)
	}
}

func TestCreateCampaign_HappyPath(t *testing.T) {
	h, pool := newMockHandler(t)
	pool.ExpectQuery(`INSERT INTO commerce\.ad_campaigns`).
		WithArgs("summer", "acme", int64(1000), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(campaignRow("ad-1", "summer", "draft", 1000))

	rec := httptest.NewRecorder()
	h.CreateCampaign(rec, campaignRequest(t, `{"name":" summer ","advertiser":"acme","budget_minor":1000}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	var c Campaign
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if c.Name != "summer" || c.Status != "draft" {
		t.Fatalf("unexpected campaign (name must be trimmed): %+v", c)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// --- status transitions (audit: ended → active resurrection) ----------------

func serveUpdateCampaign(t *testing.T, pool pgxmock.PgxPoolIface, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := &Handler{db: pool, log: zap.NewExample()}
	r := chi.NewRouter()
	r.Patch("/v1/ads/campaigns/{id}", h.UpdateCampaign)
	req := withClaims(httptest.NewRequest(http.MethodPatch, "/v1/ads/campaigns/"+id,
		strings.NewReader(body)), "ops-1", "operator")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestUpdateCampaign_EndedIsTerminal(t *testing.T) {
	_, pool := newMockHandler(t)
	pool.ExpectQuery(`SELECT status FROM commerce\.ad_campaigns`).
		WithArgs("ad-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("ended"))
	// no UPDATE expected: ended → active must be rejected

	rec := serveUpdateCampaign(t, pool, "ad-1", `{"status":"active"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "transition") {
		t.Fatalf("error body should describe the transition: %s", rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

func TestUpdateCampaign_DraftToActiveAllowed(t *testing.T) {
	_, pool := newMockHandler(t)
	pool.ExpectQuery(`SELECT status FROM commerce\.ad_campaigns`).
		WithArgs("ad-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("draft"))
	pool.ExpectQuery(`UPDATE commerce\.ad_campaigns SET status`).
		WithArgs("ad-1", "active").
		WillReturnRows(campaignRow("ad-1", "summer", "active", 1000))

	rec := serveUpdateCampaign(t, pool, "ad-1", `{"status":"active"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

func TestUpdateCampaign_NotFound(t *testing.T) {
	_, pool := newMockHandler(t)
	pool.ExpectQuery(`SELECT status FROM commerce\.ad_campaigns`).
		WithArgs("ad-x").
		WillReturnError(pgx.ErrNoRows)

	rec := serveUpdateCampaign(t, pool, "ad-x", `{"status":"active"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
