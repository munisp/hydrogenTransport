#!/bin/sh
# =============================================================================
# tigerbeetle-cluster.sh — format & run a 6-replica TigerBeetle cluster
# (replicas 0-5) for H2Fleet production.
#
# Why 6: TB recommends 3 or 5 for quorum efficiency; 6 gives quorum 4 with
# two simultaneous replica failures tolerated while preserving the View-
# change replication guarantees. For a city fleet ledger, durability >> the
# extra replica cost. (If cost matters more, run 3: set REPLICA_COUNT=3.)
#
# Usage:
#   infra/prod/tigerbeetle-cluster.sh format   # one-time: formats all datafiles
#   infra/prod/tigerbeetle-cluster.sh start    # starts tb-0 .. tb-5 (docker)
#   infra/prod/tigerbeetle-cluster.sh stop
#   infra/prod/tigerbeetle-cluster.sh status
#
# Clients: TIGERBEETLE_ADDR=tb-0:3000,tb-1:3000,tb-2:3000,tb-3:3000,tb-4:3000,tb-5:3000
#
# Requires: docker, the h2net network (main stack) or TB_NETWORK override.
# Volumes tb_data_0..5 are created on first format. FORMAT IS DESTRUCTIVE on a
# fresh volume only by construction — it refuses to re-format existing data.
# =============================================================================
set -eu

IMAGE="${TB_IMAGE:-ghcr.io/tigerbeetle/tigerbeetle:0.16.13}"
CLUSTER="${TB_CLUSTER:-0}"
REPLICA_COUNT="${TB_REPLICA_COUNT:-6}"
NETWORK="${TB_NETWORK:-infra_h2net}"
PREFIX="${TB_PREFIX:-tb}"

addresses() {
  i=0
  out=""
  while [ "$i" -lt "$REPLICA_COUNT" ]; do
    out="$out${PREFIX}-$i:3000"
    i=$((i + 1))
    [ "$i" -lt "$REPLICA_COUNT" ] && out="$out,"
  done
  printf '%s' "$out"
}

cmd_format() {
  i=0
  while [ "$i" -lt "$REPLICA_COUNT" ]; do
    vol="${PREFIX}_data_$i"
    docker volume create "$vol" >/dev/null
    # Refuse to re-format: a formatted datafile already exists if this exits 0.
    if docker run --rm -v "$vol":/data "$IMAGE" ls "/data/${CLUSTER}_${i}.tigerbeetle" >/dev/null 2>&1; then
      echo "tb-$i: datafile exists — REFUSING to re-format (delete volume $vol to force)" >&2
      exit 1
    fi
    echo "tb-$i: formatting replica $i of $REPLICA_COUNT (cluster $CLUSTER)"
    docker run --rm -v "$vol":/data "$IMAGE" format \
      --cluster="$CLUSTER" \
      --replica="$i" \
      --replica-count="$REPLICA_COUNT" \
      "/data/${CLUSTER}_${i}.tigerbeetle"
    i=$((i + 1))
  done
  echo "format complete: $REPLICA_COUNT replicas, cluster $CLUSTER"
}

cmd_start() {
  i=0
  while [ "$i" -lt "$REPLICA_COUNT" ]; do
    name="${PREFIX}-$i"
    vol="${PREFIX}_data_$i"
    if docker inspect "$name" >/dev/null 2>&1; then
      echo "$name already running"
    else
      echo "starting $name (replica $i)"
      docker run -d \
        --name "$name" \
        --network "$NETWORK" \
        -v "$vol":/data \
        --restart unless-stopped \
        "$IMAGE" start \
          --addresses="$(addresses)" \
          "/data/${CLUSTER}_${i}.tigerbeetle"
    fi
    i=$((i + 1))
  done
  echo "cluster addresses: $(addresses)"
}

cmd_stop() {
  i=0
  names=""
  while [ "$i" -lt "$REPLICA_COUNT" ]; do
    names="$names ${PREFIX}-$i"
    i=$((i + 1))
  done
  # shellcheck disable=SC2086 # intentional word splitting over container names
  docker stop $names 2>/dev/null || true
}

cmd_status() {
  docker ps --filter "name=^${PREFIX}-" --format 'table {{.Names}}\t{{.Status}}'
}

case "${1:-}" in
  format) cmd_format ;;
  start)  cmd_start ;;
  stop)   cmd_stop ;;
  status) cmd_status ;;
  *)
    echo "usage: $0 {format|start|stop|status}" >&2
    exit 2
    ;;
esac
