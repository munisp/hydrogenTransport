"""Continuous training loop: fine-tune -> evaluate vs champion -> promote if better.

Designed to run as a cron job / Temporal scheduled activity (see README:
suggested schedule nightly 02:30 + weekly full retrain). One iteration:

  1. pull the latest platform data (--source postgres by default; lakehouse
     parquet is merged automatically when --source iceberg)
  2. fine-tune each requested model FROM its current champion artifact
     (small --epochs; this is continuous training, not a from-scratch run)
  3. evaluate the challenger on held-out data (trainer's test split)
  4. promote ONLY if the primary metric improves:
       - writes challenger -> champion in registry.json
       - transitions the MLflow model version stage/alias when MLflow is on
     otherwise the champion is kept untouched and the candidate artifact is
     retained on disk for inspection.

Env: DATABASE_URL, LAKEHOUSE_DIR, MODEL_ARTIFACTS_DIR, MLFLOW_TRACKING_URI,
CT_INTERVAL_S (loop mode), CT_MODELS (comma list, default all).
"""

from __future__ import annotations

import argparse
import logging
import os
import time

from app.registry import (MODEL_NAMES, read_registry, resolve_version,
                          write_registry)
from training import datasets as ds
from training.train import TRAINERS, run_one

log = logging.getLogger("ml-platform.continuous")


def _better(candidate: dict, champion_metrics: dict | None) -> bool:
    """Primary-metric comparison honouring lower_is_better."""
    if not champion_metrics:
        return True
    metric = candidate["metrics"]["primary_metric"]
    new = candidate["metrics"]["primary_value"]
    old = champion_metrics.get(metric)
    if old is None:
        old = champion_metrics.get("primary_value")
    if old is None:
        return True
    if candidate["metrics"].get("lower_is_better"):
        return new < old
    return new > old


def _champion_metrics(artifacts_dir: str, model: str) -> dict | None:
    import json
    version = resolve_version(artifacts_dir, model, "champion")
    if not version:
        return None
    path = os.path.join(artifacts_dir, model, version, "metrics.json")
    if not os.path.exists(path):
        return None
    with open(path) as f:
        return json.load(f)


def _mlflow_transition(model: str, version: str) -> None:
    """Best-effort MLflow registry transition; graceful no-op when off."""
    if not os.environ.get("MLFLOW_TRACKING_URI"):
        return
    try:
        import mlflow
        client = mlflow.tracking.MlflowClient()
        try:
            client.set_registered_model_alias(f"h2fleet-{model}", "champion")
        except Exception:
            client.transition_model_version_stage(
                name=f"h2fleet-{model}", version=version, stage="Production",
                archive_existing_versions=True)
        log.info("mlflow: %s/%s marked champion", model, version)
    except Exception as exc:
        log.warning("mlflow transition skipped: %s", exc)


def iterate(args) -> list[dict]:
    frames = ds.load_frames(args.source, args.data_dir, args.database_url,
                            args.lakehouse_dir, days=args.days, seed=args.seed,
                            fleet_mix=getattr(args, "fleet_mix", "h2"))
    registry = read_registry(args.artifacts_dir)
    outcomes = []
    for model in args.models:
        # Resolve the champion BEFORE training: run_one saves the candidate
        # artifact to disk, and resolve_version's on-disk fallback would
        # otherwise mistake the fresh candidate for the champion.
        champion_version = resolve_version(args.artifacts_dir, model, "champion")
        champion_metrics = _champion_metrics(args.artifacts_dir, model)
        args.finetune_from = (
            os.path.join(args.artifacts_dir, model, champion_version)
            if champion_version else "")
        log.info("continuous-train %s (finetune_from=%s)", model,
                 args.finetune_from or "scratch")
        result = run_one(model, frames, args, args.device)
        promoted = _better(result, champion_metrics)
        if promoted:
            registry.setdefault("champion", {})[model] = result["version"]
            registry.setdefault("challenger", {}).pop(model, None)
            _mlflow_transition(model, result["version"])
            log.info("PROMOTED %s -> %s (%s=%.4f beats %.4f)", model,
                     result["version"], result["metrics"]["primary_metric"],
                     result["metrics"]["primary_value"],
                     (champion_metrics or {}).get("primary_value", float("nan")))
        else:
            registry.setdefault("challenger", {})[model] = result["version"]
            log.info("kept champion for %s (candidate %s did not improve %s)",
                     model, result["version"], result["metrics"]["primary_metric"])
        outcomes.append({"model": model, "candidate": result["version"],
                         "promoted": promoted,
                         "metric": result["metrics"]["primary_metric"],
                         "value": result["metrics"]["primary_value"]})
    write_registry(args.artifacts_dir, registry)
    return outcomes


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--models", default=os.environ.get("CT_MODELS", ",".join(MODEL_NAMES)))
    ap.add_argument("--source", default="postgres",
                    choices=["synth", "postgres", "iceberg"])
    ap.add_argument("--fleet-mix", default=os.environ.get("CT_FLEET_MIX", "h2"),
                    choices=["h2", "battery", "diesel", "mixed"],
                    help="energy mix for on-demand synth generation (source=synth)")
    ap.add_argument("--data-dir", default=os.environ.get("SYNTH_DATA_DIR", "data/synth_out"))
    ap.add_argument("--database-url", default=os.environ.get("DATABASE_URL", ""))
    ap.add_argument("--lakehouse-dir", default=os.environ.get("LAKEHOUSE_DIR", ""))
    ap.add_argument("--artifacts-dir", default=os.environ.get("MODEL_ARTIFACTS_DIR", "artifacts"))
    ap.add_argument("--epochs", type=int, default=3)
    ap.add_argument("--batch-size", type=int, default=512)
    ap.add_argument("--lr", type=float, default=5e-4)
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--days", type=int, default=42)
    ap.add_argument("--max-windows", type=int, default=1200)
    ap.add_argument("--device", default="cpu")
    ap.add_argument("--ray", action="store_true")
    ap.add_argument("--once", action="store_true", help="single iteration (cron mode)")
    ap.add_argument("--interval-s", type=int,
                    default=int(os.environ.get("CT_INTERVAL_S", "86400")))
    ap.add_argument("--version", default="")
    args = ap.parse_args()
    args.models = [m.strip() for m in args.models.split(",") if m.strip()]
    args.register = False      # promotion is decided HERE, not in train.py
    args.finetune_from = ""
    args.window = None

    logging.basicConfig(level=logging.INFO,
                        format="%(asctime)s %(levelname)s %(name)s %(message)s")
    if args.once:
        print(iterate(args))
        return
    log.info("continuous training loop every %ds (models=%s, source=%s)",
             args.interval_s, args.models, args.source)
    while True:
        try:
            iterate(args)
        except Exception:
            log.exception("continuous training iteration failed")
        time.sleep(args.interval_s)


if __name__ == "__main__":
    main()
