package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"go.uber.org/zap"
)

func newMockHandler(t *testing.T) (*Handler, pgxmock.PgxPoolIface) {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return &Handler{db: pool, log: zap.NewNop()}, pool
}

// GET /v1/incidents must return the {"incidents": [...]} envelope with the
// infra.incidents column mapping (SPEC §3.4) and parsed meta JSON.
func TestListIncidents_Shape(t *testing.T) {
	h, pool := newMockHandler(t)

	opened := time.Date(2026, 7, 24, 8, 30, 0, 0, time.UTC)
	meta := []byte(`{"sensor":"leak-07","ppm":1200}`)
	pool.ExpectQuery(`FROM infra\.incidents`).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "type", "severity", "bus_id", "station_id", "status", "opened_at", "meta",
		}).AddRow("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "h2_leak", "high", nil, nil, "open", opened, meta))

	rec := httptest.NewRecorder()
	h.ListIncidents(rec, httptest.NewRequest(http.MethodGet, "/v1/incidents", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var body struct {
		Incidents []Incident `json:"incidents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Incidents) != 1 {
		t.Fatalf("got %d incidents, want 1", len(body.Incidents))
	}
	i := body.Incidents[0]
	if i.ID != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" || i.Type != "h2_leak" ||
		i.Severity != "high" || i.Status != "open" || !i.OpenedAt.Equal(opened) {
		t.Fatalf("incident mapping wrong: %+v", i)
	}
	if i.Meta["sensor"] != "leak-07" {
		t.Fatalf("meta jsonb not decoded: %+v", i.Meta)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// The ?status= filter must be pushed down into the SQL query as $1.
func TestListIncidents_StatusFilter(t *testing.T) {
	h, pool := newMockHandler(t)

	pool.ExpectQuery(`WHERE status = \$1`).
		WithArgs("resolved").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "type", "severity", "bus_id", "station_id", "status", "opened_at", "meta",
		}))

	rec := httptest.NewRecorder()
	h.ListIncidents(rec, httptest.NewRequest(http.MethodGet, "/v1/incidents?status=resolved", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var body struct {
		Incidents []Incident `json:"incidents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Incidents) != 0 {
		t.Fatalf("got %d incidents, want empty list", len(body.Incidents))
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
