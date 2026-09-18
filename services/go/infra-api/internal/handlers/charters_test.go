package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"go.uber.org/zap"
)

// --- Wave-7 A2-09: charter/school block bookings -----------------------------

// recordingPub records published topics (the shared testPublisher is a
// no-op; charter tests assert the exact event stream).
type recordingPub struct {
	mu     sync.Mutex
	topics []string
}

func (p *recordingPub) Publish(_ context.Context, topic string, _ any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.topics = append(p.topics, topic)
	return nil
}
func (p *recordingPub) Close() {}
func (p *recordingPub) published() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.topics...)
}

type recordingSignaler struct {
	mu      sync.Mutex
	signals []string
}

func (s *recordingSignaler) Signal(_ context.Context, id, signal string, _ any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signals = append(s.signals, id+":"+signal)
	return nil
}
func (s *recordingSignaler) Close() {}

var (
	charterStarts = time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	charterEnds   = time.Date(2026, 9, 20, 15, 0, 0, 0, time.UTC)
	charterStamp  = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
)

var charterCols = []string{
	"id", "reference", "customer_name", "contact", "starts_at", "ends_at",
	"status", "notes", "created_by", "created_at", "cancelled_at",
}

func charterRow(status string) *pgxmock.Rows {
	return pgxmock.NewRows(charterCols).
		AddRow("bk-1", "SCH-2026-042", "Riverside School District", "trips@school.example",
			charterStarts, charterEnds, status, "field trip", "", charterStamp, nil)
}

const charterBody = `{"reference":"SCH-2026-042","customer_name":"Riverside School District",` +
	`"contact":"trips@school.example","starts_at":"2026-09-20T09:00:00Z","ends_at":"2026-09-20T15:00:00Z",` +
	`"vehicle_ids":["veh-1","veh-2"],"notes":"field trip"}`

// A two-vehicle charter creates, in ONE transaction: the booking header and
// per vehicle the overlap check, the placeholder driver, the backing
// dispatch job (route='charter:<reference>') and the charter_vehicles link.
func TestCreateCharter_Happy(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	pub := &recordingPub{}
	h := &Handler{db: pool, pub: pub, wf: testSignaler{}, log: zap.NewNop()}

	pool.ExpectBegin()
	pool.ExpectExec(`pg_advisory_xact_lock`).WithArgs("charter:SCH-2026-042").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`INSERT INTO infra\.charter_bookings`).
		WithArgs("SCH-2026-042", "Riverside School District", "trips@school.example",
			charterStarts, charterEnds, "field trip", "").
		WillReturnRows(charterRow("confirmed"))

	for i, vehicle := range []string{"veh-1", "veh-2"} {
		job := "job-" + vehicle
		_ = i
		pool.ExpectQuery(`SELECT EXISTS`).
			WithArgs(vehicle, charterStarts, charterEnds).
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
		pool.ExpectExec(`INSERT INTO infra\.drivers`).
			WithArgs("charter:SCH-2026-042:"+vehicle, "Charter placeholder SCH-2026-042").
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		pool.ExpectQuery(`INSERT INTO infra\.dispatch_jobs`).
			WithArgs("charter:SCH-2026-042:"+vehicle, vehicle, "charter:SCH-2026-042", charterStarts, charterEnds).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(job))
		pool.ExpectExec(`INSERT INTO infra\.charter_vehicles`).
			WithArgs("bk-1", vehicle, job).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
	}
	pool.ExpectCommit()

	rec := httptest.NewRecorder()
	h.CreateCharter(rec, httptest.NewRequest(http.MethodPost, "/v1/charters", strings.NewReader(charterBody)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	var b CharterBooking
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if b.Reference != "SCH-2026-042" || b.Status != "confirmed" || len(b.Vehicles) != 2 {
		t.Fatalf("unexpected booking: %+v", b)
	}
	if b.Vehicles[0].DispatchJobID != "job-veh-1" || b.Vehicles[1].DispatchJobID != "job-veh-2" {
		t.Fatalf("each vehicle must be linked to its backing dispatch job: %+v", b.Vehicles)
	}
	if topics := pub.published(); len(topics) != 1 || topics[0] != "charter.booking.confirmed" {
		t.Fatalf("published topics %v, want [charter.booking.confirmed]", topics)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// A vehicle with an overlapping active job rejects the whole booking — no
// partial reservation is left behind (transaction rolls back).
func TestCreateCharter_VehicleConflict(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, pub: &recordingPub{}, wf: testSignaler{}, log: zap.NewNop()}

	pool.ExpectBegin()
	pool.ExpectExec(`pg_advisory_xact_lock`).WithArgs("charter:SCH-2026-042").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`INSERT INTO infra\.charter_bookings`).
		WithArgs("SCH-2026-042", "Riverside School District", "trips@school.example",
			charterStarts, charterEnds, "field trip", "").
		WillReturnRows(charterRow("confirmed"))
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("veh-1", charterStarts, charterEnds).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectRollback()

	rec := httptest.NewRecorder()
	h.CreateCharter(rec, httptest.NewRequest(http.MethodPost, "/v1/charters", strings.NewReader(charterBody)))

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "veh-1") {
		t.Fatalf("conflict must name the blocked vehicle: %s", rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Retrying a create with the same reference replays the existing booking
// (200) instead of duplicating vehicles or erroring — the reference is the
// customer-facing idempotency key.
func TestCreateCharter_IdempotentReplay(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, pub: &recordingPub{}, wf: testSignaler{}, log: zap.NewNop()}

	pool.ExpectBegin()
	pool.ExpectExec(`pg_advisory_xact_lock`).WithArgs("charter:SCH-2026-042").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`INSERT INTO infra\.charter_bookings`).
		WithArgs("SCH-2026-042", "Riverside School District", "trips@school.example",
			charterStarts, charterEnds, "field trip", "").
		WillReturnError(&pgconn.PgError{Code: "23505"})
	pool.ExpectQuery(`FROM infra\.charter_bookings WHERE reference = \$1`).
		WithArgs("SCH-2026-042").
		WillReturnRows(charterRow("confirmed"))
	pool.ExpectRollback()

	rec := httptest.NewRecorder()
	h.CreateCharter(rec, httptest.NewRequest(http.MethodPost, "/v1/charters", strings.NewReader(charterBody)))

	if rec.Code != http.StatusOK {
		t.Fatalf("replay: got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// The same reference attached to a DIFFERENT booking is a real conflict.
func TestCreateCharter_ReferenceTaken(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, pub: &recordingPub{}, wf: testSignaler{}, log: zap.NewNop()}

	pool.ExpectBegin()
	pool.ExpectExec(`pg_advisory_xact_lock`).WithArgs("charter:SCH-2026-042").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`INSERT INTO infra\.charter_bookings`).
		WithArgs("SCH-2026-042", "Riverside School District", "trips@school.example",
			charterStarts, charterEnds, "field trip", "").
		WillReturnError(&pgconn.PgError{Code: "23505"})
	pool.ExpectQuery(`FROM infra\.charter_bookings WHERE reference = \$1`).
		WithArgs("SCH-2026-042").
		WillReturnRows(pgxmock.NewRows(charterCols).
			AddRow("bk-9", "SCH-2026-042", "Other Customer", "", charterStarts, charterEnds,
				"confirmed", "", "", charterStamp, nil))
	pool.ExpectRollback()

	rec := httptest.NewRecorder()
	h.CreateCharter(rec, httptest.NewRequest(http.MethodPost, "/v1/charters", strings.NewReader(charterBody)))

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// A vehicle id that does not reference a fleet vehicle fails with 422 (FK
// violation on the backing dispatch job) — never a phantom reservation.
func TestCreateCharter_UnknownVehicle(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, pub: &recordingPub{}, wf: testSignaler{}, log: zap.NewNop()}

	pool.ExpectBegin()
	pool.ExpectExec(`pg_advisory_xact_lock`).WithArgs("charter:SCH-2026-042").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`INSERT INTO infra\.charter_bookings`).
		WithArgs("SCH-2026-042", "Riverside School District", "trips@school.example",
			charterStarts, charterEnds, "field trip", "").
		WillReturnRows(charterRow("confirmed"))
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("veh-1", charterStarts, charterEnds).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	pool.ExpectExec(`INSERT INTO infra\.drivers`).
		WithArgs("charter:SCH-2026-042:veh-1", "Charter placeholder SCH-2026-042").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery(`INSERT INTO infra\.dispatch_jobs`).
		WithArgs("charter:SCH-2026-042:veh-1", "veh-1", "charter:SCH-2026-042", charterStarts, charterEnds).
		WillReturnError(&pgconn.PgError{Code: "23503"})
	pool.ExpectRollback()

	rec := httptest.NewRecorder()
	h.CreateCharter(rec, httptest.NewRequest(http.MethodPost, "/v1/charters", strings.NewReader(charterBody)))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

func TestCreateCharter_NoVehicles(t *testing.T) {
	h := &Handler{log: zap.NewNop()} // no db: must not be reached
	rec := httptest.NewRecorder()

	h.CreateCharter(rec, httptest.NewRequest(http.MethodPost, "/v1/charters",
		strings.NewReader(`{"reference":"X","customer_name":"Y","starts_at":"2026-09-20T09:00:00Z","ends_at":"2026-09-20T15:00:00Z"}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (body: %s)", rec.Code, rec.Body)
	}
}

func TestCreateCharter_DuplicateVehicle(t *testing.T) {
	h := &Handler{log: zap.NewNop()} // no db: must not be reached
	rec := httptest.NewRecorder()

	h.CreateCharter(rec, httptest.NewRequest(http.MethodPost, "/v1/charters",
		strings.NewReader(`{"reference":"X","customer_name":"Y","starts_at":"2026-09-20T09:00:00Z","ends_at":"2026-09-20T15:00:00Z","vehicle_ids":["veh-1","veh-1"]}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (body: %s)", rec.Code, rec.Body)
	}
}

// Cancelling a charter cancels the booking AND its active backing dispatch
// jobs in one transaction, then delivers the workflow's job-cancelled
// signal per job — the vehicles are free for regular dispatch immediately.
func TestCancelCharter_Happy(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	pub, sig := &recordingPub{}, &recordingSignaler{}
	h := &Handler{db: pool, pub: pub, wf: sig, log: zap.NewNop()}

	pool.ExpectBegin()
	pool.ExpectQuery(`UPDATE infra\.charter_bookings`).
		WithArgs("bk-1").
		WillReturnRows(charterRow("cancelled"))
	pool.ExpectQuery(`UPDATE infra\.dispatch_jobs`).
		WithArgs("bk-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("job-veh-1").AddRow("job-veh-2"))
	pool.ExpectCommit()

	rec := httptest.NewRecorder()
	req := withURLParams(httptest.NewRequest(http.MethodPost, "/v1/charters/bk-1/cancel", nil), "id", "bk-1")
	h.CancelCharter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if topics := pub.published(); len(topics) != 1 || topics[0] != "charter.booking.cancelled" {
		t.Fatalf("published topics %v, want [charter.booking.cancelled]", topics)
	}
	if len(sig.signals) != 2 {
		t.Fatalf("each cancelled job must get the workflow cancel signal, got %v", sig.signals)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

func TestCancelCharter_NotConfirmed(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, pub: &recordingPub{}, wf: testSignaler{}, log: zap.NewNop()}

	pool.ExpectBegin()
	pool.ExpectQuery(`UPDATE infra\.charter_bookings`).
		WithArgs("bk-1").
		WillReturnRows(pgxmock.NewRows(charterCols)) // no row → not confirmed
	pool.ExpectRollback()

	rec := httptest.NewRecorder()
	req := withURLParams(httptest.NewRequest(http.MethodPost, "/v1/charters/bk-1/cancel", nil), "id", "bk-1")
	h.CancelCharter(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
