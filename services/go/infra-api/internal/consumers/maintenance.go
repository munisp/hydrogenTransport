// Package consumers hosts the Kafka consumers that close the platform's
// event loops (BUSINESS_LOGIC_AUDIT: events produced-never-consumed).
package consumers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.uber.org/zap"
)

// DB is the subset of the pgx pool the consumers need (mockable in tests).
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// MaintenancePredicted is the data payload of a maintenance.predicted event
// (services/python/predictive-maintenance/app/events.py).
type MaintenancePredicted struct {
	PredictionID       string  `json:"prediction_id"`
	BusID              string  `json:"bus_id"`
	Component          string  `json:"component"`
	RiskScore          float64 `json:"risk_score"`
	PredictedFailureAt string  `json:"predicted_failure_at"`
	ModelVersion       string  `json:"model_version"`
}

type envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// WorkOrderTitle renders the operator-facing title for a prediction-backed
// work order.
func WorkOrderTitle(p MaintenancePredicted) string {
	return fmt.Sprintf("Predicted %s failure risk %.2f (bus %s)", p.Component, p.RiskScore, p.BusID)
}

// CreateWorkOrderFromPrediction inserts the prediction-backed work order.
// Idempotent: the open-prediction partial unique index (migration 0007) +
// ON CONFLICT DO NOTHING make replays and redeliveries safe — at most one
// open work order exists per prediction.
func CreateWorkOrderFromPrediction(ctx context.Context, db DB, p MaintenancePredicted) error {
	if p.PredictionID == "" || p.BusID == "" || p.Component == "" {
		return errors.New("maintenance.predicted payload missing prediction_id/bus_id/component")
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO infra.work_orders (title, description, asset_ref, bus_id, prediction_id)
		VALUES ($1, $2, $3, $4::uuid, $5::uuid)
		ON CONFLICT (prediction_id) WHERE status <> 'closed' DO NOTHING`,
		WorkOrderTitle(p),
		fmt.Sprintf("Auto-created from maintenance.predicted: component=%s risk=%.3f predicted_failure_at=%s model=%s",
			p.Component, p.RiskScore, p.PredictedFailureAt, p.ModelVersion),
		"fleet:"+p.BusID,
		p.BusID,
		p.PredictionID); err != nil {
		return fmt.Errorf("insert prediction work order: %w", err)
	}
	return nil
}

// StartMaintenanceConsumer consumes maintenance.predicted and creates depot
// work orders for high-risk predictions, closing the loop described by
// BUSINESS_LOGIC_AUDIT §2. Offsets are committed only after the work order
// is durably written, so a crash redelivers instead of losing the
// prediction. It runs until ctx is cancelled. brokers empty is a no-op.
func StartMaintenanceConsumer(ctx context.Context, brokers string, db DB, log *zap.Logger) {
	if strings.TrimSpace(brokers) == "" {
		log.Warn("KAFKA_BROKERS not set; maintenance.predicted consumer disabled")
		return
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(strings.Split(brokers, ",")...),
		kgo.ConsumerGroup("infra-api-maintenance-wo"),
		kgo.ConsumeTopics("maintenance.predicted"),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		log.Error("maintenance consumer init failed", zap.Error(err))
		return
	}
	defer client.Close()
	log.Info("maintenance.predicted consumer started")

	for {
		if ctx.Err() != nil {
			return
		}
		fetches := client.PollFetches(ctx)
		if errs := fetches.Errors(); len(errs) > 0 {
			if ctx.Err() != nil {
				return
			}
			for _, e := range errs {
				log.Error("maintenance.predicted fetch error", zap.Error(e.Err))
			}
			time.Sleep(2 * time.Second)
			continue
		}
		fetches.EachRecord(func(rec *kgo.Record) {
			var env envelope
			if err := json.Unmarshal(rec.Value, &env); err != nil {
				log.Warn("dropping malformed maintenance.predicted message", zap.Error(err))
				markCommitted(client, rec, log)
				return
			}
			var p MaintenancePredicted
			if err := json.Unmarshal(env.Data, &p); err != nil {
				log.Warn("dropping maintenance.predicted with bad data payload", zap.Error(err))
				markCommitted(client, rec, log)
				return
			}
			if err := CreateWorkOrderFromPrediction(ctx, db, p); err != nil {
				// Not committed: redelivery after the error clears.
				log.Error("work order creation from prediction failed", zap.Error(err))
				return
			}
			markCommitted(client, rec, log)
		})
	}
}

func markCommitted(client *kgo.Client, rec *kgo.Record, log *zap.Logger) {
	if err := client.CommitRecords(context.Background(), rec); err != nil {
		log.Error("offset commit failed", zap.Error(err))
	}
}
