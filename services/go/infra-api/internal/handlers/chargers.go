package handlers

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Wave 5 (0008): OCPP charger read APIs. infra.charge_points and
// infra.charging_sessions are written by the ocpp-gateway (W4) from OCPP
// 1.6J messages (BootNotification/Heartbeat/StatusNotification/
// StartTransaction/StopTransaction); this service only reads them.

// ChargePoint mirrors infra.charge_points.
type ChargePoint struct {
	ID            string     `json:"id"`
	StationID     string     `json:"station_id"`
	OcppID        string     `json:"ocpp_id"`
	Vendor        string     `json:"vendor"`
	Model         string     `json:"model"`
	Status        string     `json:"status"` // OCPP 1.6J charge-point status (Available|Charging|Unavailable|...)
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

const chargePointCols = `id, station_id, ocpp_id, COALESCE(vendor,''), COALESCE(model,''),
	COALESCE(status,'Unavailable'), last_heartbeat, created_at`

func scanChargePoint(row pgx.Row) (ChargePoint, error) {
	var cp ChargePoint
	err := row.Scan(&cp.ID, &cp.StationID, &cp.OcppID, &cp.Vendor, &cp.Model,
		&cp.Status, &cp.LastHeartbeat, &cp.CreatedAt)
	return cp, err
}

// ChargingSession mirrors infra.charging_sessions.
type ChargingSession struct {
	ID            string     `json:"id"`
	ChargePointID string     `json:"charge_point_id"`
	BusID         *string    `json:"bus_id,omitempty"`
	ConnectorID   int        `json:"connector_id"`
	IDTag         *string    `json:"id_tag,omitempty"`
	MeterStart    float64    `json:"meter_start"`
	MeterStop     *float64   `json:"meter_stop,omitempty"`
	Kwh           *float64   `json:"kwh,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	StoppedAt     *time.Time `json:"stopped_at,omitempty"`
	Status        string     `json:"status"` // active|completed|failed
}

func scanChargingSession(row pgx.Row) (ChargingSession, error) {
	var s ChargingSession
	err := row.Scan(&s.ID, &s.ChargePointID, &s.BusID, &s.ConnectorID, &s.IDTag,
		&s.MeterStart, &s.MeterStop, &s.Kwh, &s.StartedAt, &s.StoppedAt, &s.Status)
	return s, err
}

func (h *Handler) listChargePoints(w http.ResponseWriter, r *http.Request, where string, args ...any) {
	query := `SELECT ` + chargePointCols + ` FROM infra.charge_points`
	if where != "" {
		query += ` WHERE ` + where
	}
	query += ` ORDER BY ocpp_id LIMIT 500`
	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		h.internal(w, "list charge points", err)
		return
	}
	defer rows.Close()

	points := []ChargePoint{}
	for rows.Next() {
		cp, err := scanChargePoint(rows)
		if err != nil {
			h.internal(w, "scan charge point", err)
			return
		}
		points = append(points, cp)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate charge points", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"charge_points": points})
}

// ListStationChargers handles GET /v1/stations/{id}/chargers (refueling-stations
// module): all OCPP charge points installed at one station.
func (h *Handler) ListStationChargers(w http.ResponseWriter, r *http.Request) {
	h.listChargePoints(w, r, `station_id = $1`, chi.URLParam(r, "id"))
}

// ListChargers handles GET /v1/chargers (?station_id= optional): the
// fleet-wide charge-point inventory with live OCPP status.
func (h *Handler) ListChargers(w http.ResponseWriter, r *http.Request) {
	if stationID := r.URL.Query().Get("station_id"); stationID != "" {
		h.listChargePoints(w, r, `station_id = $1`, stationID)
		return
	}
	h.listChargePoints(w, r, "")
}

// ListChargerSessions handles GET /v1/chargers/{ocpp_id}/sessions: charging
// sessions for one charge point, newest first (?status= filters, e.g.
// active|completed).
func (h *Handler) ListChargerSessions(w http.ResponseWriter, r *http.Request) {
	ocppID := chi.URLParam(r, "ocpp_id")

	args := []any{ocppID}
	where := `cp.ocpp_id = $1`
	if status := r.URL.Query().Get("status"); status != "" {
		args = append(args, status)
		where += ` AND s.status = $2`
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT s.id, s.charge_point_id, s.bus_id, s.connector_id, s.id_tag,
		       COALESCE(s.meter_start,0), s.meter_stop, s.kwh, s.started_at, s.stopped_at,
		       COALESCE(s.status,'active')
		FROM infra.charging_sessions s
		JOIN infra.charge_points cp ON cp.id = s.charge_point_id
		WHERE `+where+`
		ORDER BY s.started_at DESC LIMIT 200`, args...)
	if err != nil {
		h.internal(w, "list charging sessions", err)
		return
	}
	defer rows.Close()

	sessions := []ChargingSession{}
	for rows.Next() {
		s, err := scanChargingSession(rows)
		if err != nil {
			h.internal(w, "scan charging session", err)
			return
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate charging sessions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}
