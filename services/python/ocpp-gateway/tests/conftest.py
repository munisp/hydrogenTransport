import os
import sys
import uuid

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

# ---------------------------------------------------------------- fakes
# In-memory stand-ins for the asyncpg pool and the Kafka publisher, scripted
# off the SQL strings used by app.db (same approach as carbon-analytics
# tests' _FakePool). They implement just enough relational behaviour to test
# the CSMS write paths truthfully (upsert, status update, session lifecycle,
# kwh math executed with the app's SQL parameters).


class FakePublisher:
    def __init__(self):
        self.published: list[tuple[dict, str]] = []
        self.connected = True

    async def publish(self, data: dict, key: str) -> bool:
        self.published.append((data, key))
        return True

    async def aclose(self):
        pass


class FakePool:
    def __init__(self, *, station_id=None, available_kwh=None, known_buses=None, fail=False):
        self.station_id = station_id or str(uuid.uuid4())
        self.available_kwh = available_kwh
        self.known_buses = dict(known_buses or {})  # id_tag -> bus uuid
        self.fail = fail
        self.charge_points: dict[str, dict] = {}
        self.sessions: dict[str, dict] = {}
        self.heartbeats = 0

    # ------------------------------------------------------------ internals
    def _upsert_cp(self, ocpp_id, vendor, model):
        cp = self.charge_points.get(ocpp_id)
        if cp is None:
            cp = {
                "id": str(uuid.uuid4()),
                "station_id": self.station_id,
                "ocpp_id": ocpp_id,
                "created_at": "2026-01-01T00:00:00Z",
            }
            self.charge_points[ocpp_id] = cp
        cp.update(vendor=vendor, model=model, status="Available", last_heartbeat="now")
        return {"id": cp["id"], "station_id": cp["station_id"]}

    # ------------------------------------------------------------ asyncpg API
    async def fetchval(self, sql, *args):
        if self.fail:
            raise RuntimeError("db down")
        if "SELECT 1" in sql:
            return 1
        if "FROM infra.charge_points" in sql and "WHERE ocpp_id" in sql:
            cp = self.charge_points.get(args[0])
            return cp["id"] if cp else None
        if "FROM fleet.vehicles" in sql:
            return self.known_buses.get(args[0])
        raise AssertionError(f"unexpected fetchval SQL: {sql}")

    async def fetchrow(self, sql, *args):
        if self.fail:
            raise RuntimeError("db down")
        if "INSERT INTO infra.charge_points" in sql:
            return self._upsert_cp(args[0], args[1], args[2])
        if "UPDATE infra.charge_points SET status" in sql:
            cp = self.charge_points.get(args[0])
            if cp is None:
                return None
            cp["status"] = args[1]
            return {"id": cp["id"], "station_id": cp["station_id"], "available_kwh": self.available_kwh}
        if "INSERT INTO infra.charging_sessions" in sql:
            sid = str(uuid.uuid4())
            self.sessions[sid] = {
                "id": sid,
                "charge_point_id": args[0],
                "bus_id": args[1],
                "connector_id": args[2],
                "id_tag": args[3],
                "meter_start": args[4],
                "meter_stop": None,
                "kwh": None,
                "status": "active",
            }
            return {"id": sid}
        if "UPDATE infra.charging_sessions" in sql and "meter_stop" in sql:
            s = self.sessions.get(args[0])
            if s is None or s["status"] != "active":
                return None
            meter_stop, factor = args[1], args[2]
            s["meter_stop"] = meter_stop
            s["kwh"] = (meter_stop - s["meter_start"]) * factor
            s["status"] = "completed"
            return {"id": s["id"], "kwh": s["kwh"]}
        if "FROM infra.charge_points WHERE ocpp_id" in sql:
            return self.charge_points.get(args[0])
        raise AssertionError(f"unexpected fetchrow SQL: {sql}")

    async def execute(self, sql, *args):
        if self.fail:
            raise RuntimeError("db down")
        if "last_heartbeat" in sql:
            self.heartbeats += 1
            return
        if "SET kwh" in sql:
            sid, register, factor = args
            s = self.sessions[sid]
            if s["status"] == "active":
                s["kwh"] = (register - s["meter_start"]) * factor
            return
        raise AssertionError(f"unexpected execute SQL: {sql}")

    async def fetch(self, sql, *args):
        if self.fail:
            raise RuntimeError("db down")
        if "FROM infra.charging_sessions" in sql:
            station_id, status, limit = args
            out = []
            for s in self.sessions.values():
                cp = next((c for c in self.charge_points.values() if c["id"] == s["charge_point_id"]), None)
                if cp is None:
                    continue
                if station_id is not None and cp["station_id"] != station_id:
                    continue
                if status is not None and s["status"] != status:
                    continue
                out.append({**s, "ocpp_id": cp["ocpp_id"], "station_id": cp["station_id"]})
            return out[:limit]
        if "FROM infra.charge_points" in sql:
            return list(self.charge_points.values())
        raise AssertionError(f"unexpected fetch SQL: {sql}")
