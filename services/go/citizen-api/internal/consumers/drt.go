// Package consumers closes the citizen-domain event loops
// (BUSINESS_LOGIC_AUDIT §13: drt.requested was produced-never-consumed and
// no assignment logic existed anywhere).
package consumers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/citizen-api/internal/handlers"
)

// DRTRequested is the data payload of a drt.requested event
// (services/go/citizen-api/internal/handlers/drt.go).
type DRTRequested struct {
	RequestID string `json:"request_id"`
	UserSub   string `json:"user_sub"`
	Pickup    struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"pickup"`
	RequestedAt string `json:"requested_at"`
}

type envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Publisher is the event publisher the consumer uses for drt.assigned.
type Publisher interface {
	Publish(ctx context.Context, topic string, payload any) error
}

// PickVehicle selects the vehicle for a ride: the nearest active vehicle
// (by latest telemetry position) that is not already assigned to an active
// DRT request. Vehicles with no telemetry yet rank last. pgx.ErrNoRows =
// nobody available right now.
func PickVehicle(ctx context.Context, db handlers.DB, lat, lon float64) (string, error) {
	var vehicleID string
	err := db.QueryRow(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (bus_id) bus_id, geom
			FROM fleet.telemetry ORDER BY bus_id, ts DESC
		)
		SELECT v.id FROM fleet.vehicles v
		LEFT JOIN latest t ON t.bus_id = v.id
		WHERE v.status = 'active'
		  AND NOT EXISTS (
			SELECT 1 FROM citizen.drt_requests r
			WHERE r.vehicle_id = v.id AND r.status IN ('assigned','enroute')
		  )
		ORDER BY ST_Distance(t.geom::geography,
			ST_SetSRID(ST_MakePoint($2, $1), 4326)::geography) NULLS LAST
		LIMIT 1`, lat, lon).Scan(&vehicleID)
	return vehicleID, err
}

// AutoAssign handles one drt.requested event: pick the nearest available
// vehicle and assign it, publishing drt.assigned. When no vehicle is
// available the request honestly stays 'requested' (the operator can assign
// it later via POST /v1/drt/requests/{id}/assign) — never a fabricated
// assignment.
func AutoAssign(ctx context.Context, db handlers.DB, pub Publisher, e DRTRequested, log *zap.Logger) error {
	if e.RequestID == "" {
		return errors.New("drt.requested payload missing request_id")
	}
	vehicleID, err := PickVehicle(ctx, db, e.Pickup.Lat, e.Pickup.Lon)
	if errors.Is(err, pgx.ErrNoRows) {
		log.Info("no vehicle available for DRT request; leaving requested",
			zap.String("request", e.RequestID))
		return nil
	}
	if err != nil {
		return err
	}
	if err := handlers.AssignDRT(ctx, db, e.RequestID, vehicleID, ""); err != nil {
		if errors.Is(err, handlers.ErrDRTNotAssignable) {
			return nil // raced with another assignment/cancel — done
		}
		return err
	}
	if pub != nil {
		if err := pub.Publish(ctx, "drt.assigned", map[string]any{
			"request_id":  e.RequestID,
			"user_sub":    e.UserSub,
			"vehicle_id":  vehicleID,
			"assigned_at": time.Now().UTC().Format(time.RFC3339),
			"auto":        true,
		}); err != nil {
			log.Error("failed to publish drt.assigned", zap.Error(err))
		}
	}
	return nil
}

// StartDRTConsumer consumes drt.requested and auto-assigns vehicles.
// Offsets commit after the assignment attempt resolves (assignment is
// idempotent per request, so redelivery is safe). brokers empty is a no-op.
func StartDRTConsumer(ctx context.Context, brokers string, db handlers.DB, pub Publisher, log *zap.Logger) {
	if strings.TrimSpace(brokers) == "" {
		log.Warn("KAFKA_BROKERS not set; drt.requested consumer disabled")
		return
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(strings.Split(brokers, ",")...),
		kgo.ConsumerGroup("citizen-api-drt-assign"),
		kgo.ConsumeTopics("drt.requested"),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		log.Error("drt consumer init failed", zap.Error(err))
		return
	}
	defer client.Close()
	log.Info("drt.requested consumer started")

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
				log.Error("drt.requested fetch error", zap.Error(e.Err))
			}
			time.Sleep(2 * time.Second)
			continue
		}
		fetches.EachRecord(func(rec *kgo.Record) {
			var env envelope
			if err := json.Unmarshal(rec.Value, &env); err != nil {
				log.Warn("dropping malformed drt.requested message", zap.Error(err))
				commit(client, rec, log)
				return
			}
			var e DRTRequested
			if err := json.Unmarshal(env.Data, &e); err != nil {
				log.Warn("dropping drt.requested with bad data payload", zap.Error(err))
				commit(client, rec, log)
				return
			}
			if err := AutoAssign(ctx, db, pub, e, log); err != nil {
				log.Error("drt auto-assignment failed", zap.Error(err))
				return // uncommitted → redelivered
			}
			commit(client, rec, log)
		})
	}
}

func commit(client *kgo.Client, rec *kgo.Record, log *zap.Logger) {
	if err := client.CommitRecords(context.Background(), rec); err != nil {
		log.Error("offset commit failed", zap.Error(err))
	}
}
