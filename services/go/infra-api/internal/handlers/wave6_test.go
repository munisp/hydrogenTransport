package handlers

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// --- Wave-6 A4-05: hours of service -------------------------------------------

// A 12-hour shift exceeds DISPATCH_MAX_SHIFT_HOURS (default 10) → 422
// before any transaction.
func TestCreateDispatchJob_ShiftTooLong(t *testing.T) {
	h, _ := newDispatchHandler(t)
	rec := createJob(t, h, `{"driver_sub":"driver-1","route":"R10","starts_at":"2026-07-26T06:00:00Z","ends_at":"2026-07-26T18:00:00Z"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "hours-of-service") {
		t.Fatalf("error should name the hours-of-service limit: %s", rec.Body)
	}
}

// 8h already scheduled + a new 6h job exceeds the 10h daily cap → 422.
func TestCreateDispatchJob_DailyHoursExceeded(t *testing.T) {
	h, pool := newDispatchHandler(t)
	starts := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)

	pool.ExpectQuery(`sum\(EXTRACT\(EPOCH`).
		WithArgs("driver-1", starts, 10, "UTC").
		WillReturnRows(pgxmock.NewRows([]string{"sum"}).AddRow(8.0))

	rec := createJob(t, h, `{"driver_sub":"driver-1","route":"R10","starts_at":"2026-07-26T14:00:00Z","ends_at":"2026-07-26T20:00:00Z"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "daily hours-of-service") {
		t.Fatalf("error should name the daily limit: %s", rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// --- Wave-6 A4-02: incident type enum + severity floor ------------------------

// The type enum rejects free text — a typo must never bypass type-matched
// escalation rules.
func TestIncidentTypeEnum(t *testing.T) {
	for typ, want := range map[string]bool{
		"h2_leak": true, "collision": true, "fire": true, "prd_vent": true,
		"evacuation": true, "sos": true, "security": true, "breakdown": true, "other": true,
	} {
		if _, ok := incidentTypeFloor[typ]; ok != want {
			t.Fatalf("type %q: enum membership = %v, want %v", typ, ok, want)
		}
	}
	for _, bad := range []string{"acident", "leak", "", "SOS", "collision "} {
		if _, ok := incidentTypeFloor[bad]; ok {
			t.Fatalf("type %q must not be in the enum", bad)
		}
	}
}

// Severity floors: sos/fire are always critical; collision/prd_vent/
// evacuation/security never below high; caller escalation above the floor
// is preserved; unknown severities normalize to medium then floor.
func TestApplySeverityFloor(t *testing.T) {
	cases := []struct{ typ, in, want string }{
		{"sos", "low", "critical"},
		{"fire", "medium", "critical"},
		{"collision", "low", "high"},
		{"prd_vent", "medium", "high"},
		{"evacuation", "", "high"},
		{"security", "low", "high"},
		{"breakdown", "low", "low"},
		{"h2_leak", "critical", "critical"},
		{"collision", "critical", "critical"},
		{"other", "bogus", "medium"},
	}
	for _, c := range cases {
		if got := applySeverityFloor(c.typ, c.in); got != c.want {
			t.Fatalf("applySeverityFloor(%q,%q) = %q, want %q", c.typ, c.in, got, c.want)
		}
	}
}
