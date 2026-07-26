package consumers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type fakeDB struct {
	lastSQL  string
	lastArgs []any
	err      error
}

func (f *fakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.lastSQL, f.lastArgs = sql, args
	return pgconn.NewCommandTag("INSERT 0 1"), f.err
}

// A high-risk prediction becomes a depot work order linked to the bus and
// the prediction (BUSINESS_LOGIC_AUDIT §2 closed loop).
func TestCreateWorkOrderFromPrediction(t *testing.T) {
	db := &fakeDB{}
	p := MaintenancePredicted{
		PredictionID:       "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		BusID:              "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		Component:          "fuel_cell",
		RiskScore:          0.83,
		PredictedFailureAt: "2026-08-01T00:00:00Z",
		ModelVersion:       "rules-v1",
	}
	if err := CreateWorkOrderFromPrediction(context.Background(), db, p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(db.lastSQL, "ON CONFLICT (prediction_id)") {
		t.Fatalf("insert must be idempotent on the open-prediction index: %s", db.lastSQL)
	}
	// args: title, description, asset_ref, bus_id, prediction_id
	if db.lastArgs[3] != p.BusID || db.lastArgs[4] != p.PredictionID {
		t.Fatalf("work order must link bus + prediction: %v", db.lastArgs)
	}
	if !strings.Contains(db.lastArgs[0].(string), "fuel_cell") {
		t.Fatalf("title should name the component: %v", db.lastArgs[0])
	}
}

// Incomplete payloads are rejected (and thus not offset-committed →
// redelivered) instead of writing a broken work order.
func TestCreateWorkOrderFromPrediction_MissingFields(t *testing.T) {
	db := &fakeDB{}
	if err := CreateWorkOrderFromPrediction(context.Background(), db, MaintenancePredicted{}); err == nil {
		t.Fatal("expected error for empty payload")
	}
	if db.lastSQL != "" {
		t.Fatal("no SQL should run for an invalid payload")
	}
}

// DB failures propagate so the consumer leaves the offset uncommitted.
func TestCreateWorkOrderFromPrediction_DBError(t *testing.T) {
	db := &fakeDB{err: errors.New("connection reset")}
	p := MaintenancePredicted{PredictionID: "a", BusID: "b", Component: "tank_valve"}
	if err := CreateWorkOrderFromPrediction(context.Background(), db, p); err == nil {
		t.Fatal("expected db error to propagate")
	}
}
