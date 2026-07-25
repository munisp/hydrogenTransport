"""Training entrypoint for all H2Fleet ml-platform models (CPU-first).

Examples
--------
    python -m training.train --model all --source synth --epochs 5
    python -m training.train --model maintenance_lstm --source postgres \
        --database-url postgresql://... --finetune-from artifacts/maintenance_lstm/v1.0.0
    MLFLOW_TRACKING_URI=http://localhost:5000 python -m training.train --model all

Flags
-----
--model         one of the five model names, or "all"
--source        synth | postgres | iceberg
--finetune-from existing artifact dir (or <model>/<version> under --artifacts-dir)
                for continuous training; architecture must match
--device        cpu (default) | cuda
--ray           run the train function through ray (ray.train/remote) when the
                ray package is installed; automatic local fallback otherwise
--register      also write the trained version as champion in registry.json

MLflow: when MLFLOW_TRACKING_URI is set AND the mlflow package is installed,
params/metrics/artifacts are logged; otherwise this is a graceful no-op.
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import time

import numpy as np
import torch
from torch import nn

from app.registry import (MODEL_CLASSES, MODEL_NAMES, artifact_dir,
                          new_version, read_registry, save_artifact,
                          write_registry)
from models import (CARBON_FEATURES, DEMAND_FEATURES, GRAPH_NODE_FEATURES,
                    LEAK_SENSOR_FEATURES, SEQ_FEATURES, normalize_adjacency)
from models.maintenance_lstm import COMPONENTS
from training import datasets as ds

log = logging.getLogger("ml-platform.train")


# ------------------------------------------------------------------ helpers --
def _t(arr: np.ndarray, device: str) -> torch.Tensor:
    return torch.tensor(np.asarray(arr), dtype=torch.float32, device=device)


def baseline_histograms(X: np.ndarray, features: list[str], bins: int = 10) -> dict:
    """Quantile-bin baseline proportions per feature for the drift monitor."""
    flat = X.reshape(-1, X.shape[-1]) if X.ndim == 3 else X
    out = {}
    for i, f in enumerate(features):
        col = flat[:, i]
        edges = np.quantile(col, np.linspace(0, 1, bins + 1))
        edges[0], edges[-1] = -np.inf, np.inf
        counts, edges = np.histogram(col, bins=edges)
        out[f] = {"bin_edges": [float(e) for e in edges[1:-1]],
                  "proportions": (counts / max(counts.sum(), 1)).round(6).tolist()}
    return out


class _MLflow:
    """Graceful MLflow wrapper: no-op unless URI set and package installed."""

    def __init__(self, model: str, args: argparse.Namespace):
        self.run = None
        self.client = None
        uri = os.environ.get("MLFLOW_TRACKING_URI", "")
        if not uri:
            return
        try:
            import mlflow  # noqa: F401
            mlflow.set_tracking_uri(uri)
            mlflow.set_experiment("h2fleet-ml-platform")
            self._mlflow = mlflow
            self.run = mlflow.start_run(run_name=f"{model}-{args.source}")
            mlflow.log_params({"model": model, "source": args.source,
                               "epochs": args.epochs, "lr": args.lr,
                               "batch_size": args.batch_size, "seed": args.seed,
                               "finetune_from": args.finetune_from or ""})
            log.info("mlflow run started (uri=%s)", uri)
        except Exception as exc:  # package missing or server unreachable
            log.warning("mlflow disabled (%s)", exc)
            self.run = None

    def log_metrics(self, metrics: dict) -> None:
        if self.run:
            try:
                self._mlflow.log_metrics({k: float(v) for k, v in metrics.items()
                                          if isinstance(v, (int, float))})
            except Exception as exc:
                log.warning("mlflow metric logging failed: %s", exc)

    def log_artifacts(self, path: str, model: str, version: str) -> None:
        if self.run:
            try:
                self._mlflow.log_artifacts(path, artifact_path=f"{model}/{version}")
                self._mlflow.set_tag("h2fleet.model", model)
                self._mlflow.set_tag("h2fleet.version", version)
                self._mlflow.end_run()
            except Exception as exc:
                log.warning("mlflow artifact logging failed: %s", exc)


def _maybe_ray(train_fn, args: argparse.Namespace):
    """Run train_fn through ray when --ray and ray installed; else local."""
    if not args.ray:
        return train_fn()
    try:
        import ray
    except ImportError:
        log.warning("--ray requested but ray is not installed; running locally")
        return train_fn()
    if not ray.is_initialized():
        ray.init(ignore_reinit_error=True, include_dashboard=False,
                 log_to_driver=False)
    return ray.get(ray.remote(num_cpus=2)(train_fn).remote())


def _load_finetune(model: str, net: nn.Module, finetune_from: str,
                   artifacts_dir: str) -> None:
    path = finetune_from
    if not os.path.isdir(path):
        path = os.path.join(artifacts_dir, finetune_from)
    blob = torch.load(os.path.join(path, "weights.pt"), map_location="cpu",
                      weights_only=True)
    net.load_state_dict(blob["state_dict"])
    log.info("fine-tuning from %s", path)


# ------------------------------------------------------------- model trainers --
def train_maintenance(frames, args, device) -> tuple[nn.Module, dict, dict, dict]:
    window = args.window or 48
    X, risk_y, days_y, groups = ds.build_maintenance(
        frames["telemetry"], window=window, max_windows_per_bus=args.max_windows,
        seed=args.seed)
    m_tr, m_va, m_te = ds.split_by_group(groups, seed=args.seed)
    stats = ds.feature_stats_from_tensor(X[m_tr], SEQ_FEATURES)
    Xn = ds.zscore(X, stats, SEQ_FEATURES)

    net = MODEL_CLASSES["maintenance_lstm"]()
    if args.finetune_from:
        _load_finetune("maintenance_lstm", net, args.finetune_from, args.artifacts_dir)
    net.to(device)
    opt = torch.optim.Adam(net.parameters(), lr=args.lr)
    bce, mse = nn.BCELoss(), nn.MSELoss()

    def batches(idx):
        perm = np.random.default_rng(args.seed).permutation(np.where(idx)[0])
        for i in range(0, len(perm), args.batch_size):
            yield perm[i:i + args.batch_size]

    best_val = float("inf")
    best_state = None
    for epoch in range(args.epochs):
        net.train()
        for b in batches(m_tr):
            opt.zero_grad()
            risk, days = net(_t(Xn[b], device))
            loss = bce(risk, _t(risk_y[b], device)) \
                + 0.1 * mse(days / 60.0, _t(days_y[b], device) / 60.0)
            loss.backward()
            opt.step()
        net.eval()
        with torch.no_grad():
            vr, vd = net(_t(Xn[m_va], device))
            val = bce(vr, _t(risk_y[m_va], device)).item() \
                + 0.1 * mse(vd / 60.0, _t(days_y[m_va], device) / 60.0).item()
        log.info("epoch %d val_loss=%.4f", epoch, val)
        if val < best_val:
            best_val, best_state = val, {k: v.clone() for k, v in net.state_dict().items()}
    if best_state:
        net.load_state_dict(best_state)

    net.eval()
    with torch.no_grad():
        risk, days = net(_t(Xn[m_te], device))
    risk_np, days_np = risk.cpu().numpy(), days.cpu().numpy()
    aucs = {}
    for i, c in enumerate(COMPONENTS):
        y = risk_y[m_te][:, i]
        aucs[c] = _safe_auc(y, risk_np[:, i])
    mae_days = float(np.abs(days_np - days_y[m_te]).mean())
    metrics = {"auc_mean": float(np.nanmean(list(aucs.values()))),
               "auc_per_component": aucs, "mae_days_to_failure": mae_days,
               "primary_metric": "auc_mean", "primary_value": float(np.nanmean(list(aucs.values())))}
    schema = {"features": SEQ_FEATURES, "stats": stats, "window": window,
              "baseline": baseline_histograms(X[m_tr], SEQ_FEATURES),
              "extra": {"components": COMPONENTS}}
    return net, metrics, schema, {"n_features": len(SEQ_FEATURES)}


def _safe_auc(y_true, y_score) -> float:
    try:
        from sklearn.metrics import roc_auc_score
        if len(np.unique(y_true)) < 2:
            return float("nan")
        return float(roc_auc_score(y_true, y_score))
    except Exception:
        return float("nan")


def train_demand(frames, args, device):
    """Per-window ridership scaling: the ridership channel and the target are
    divided by the window's own mean ridership, so the forecaster learns the
    diurnal/weekly SHAPE and generalises across route scales (and to routes
    unseen at training time). Serving replicates this (schema extra flag)."""
    X, Y, _routes, ends = ds.build_demand(frames["ridership"], stride=6)
    m_tr, m_va, m_te = ds.split_chronological(ends)
    scale = np.maximum(X[:, :, 0].mean(axis=1), 1.0)[:, None]      # (N,1)
    X = X.copy(); X[:, :, 0] /= scale
    Yn = Y / scale
    stats = ds.feature_stats_from_tensor(X[m_tr], DEMAND_FEATURES)
    Xn = ds.zscore(X, stats, DEMAND_FEATURES)

    net = MODEL_CLASSES["demand_forecaster"]()
    if args.finetune_from:
        _load_finetune("demand_forecaster", net, args.finetune_from, args.artifacts_dir)
    net.to(device)
    opt = torch.optim.Adam(net.parameters(), lr=args.lr)
    mse = nn.MSELoss()
    rng = np.random.default_rng(args.seed)
    tr_idx = np.where(m_tr)[0]
    best_val, best_state = float("inf"), None
    for epoch in range(args.epochs):
        net.train()
        rng.shuffle(tr_idx)
        for i in range(0, len(tr_idx), args.batch_size):
            b = tr_idx[i:i + args.batch_size]
            opt.zero_grad()
            loss = mse(net(_t(Xn[b], device)), _t(Yn[b], device))
            loss.backward()
            opt.step()
        net.eval()
        with torch.no_grad():
            val = mse(net(_t(Xn[m_va], device)), _t(Yn[m_va], device)).item()
        log.info("epoch %d val_loss=%.4f", epoch, val)
        if val < best_val:
            best_val, best_state = val, {k: v.clone() for k, v in net.state_dict().items()}
    if best_state:
        net.load_state_dict(best_state)
    net.eval()
    with torch.no_grad():
        pred = net(_t(Xn[m_te], device)).cpu().numpy() * scale[m_te]
    err = pred - Y[m_te]
    rel = float(np.abs(err).mean() / max(Y[m_te].mean(), 1e-9))
    metrics = {"mae_riders": float(np.abs(err).mean()),
               "rmse_riders": float(np.sqrt((err ** 2).mean())),
               "rel_mae": rel,
               "primary_metric": "rel_mae",
               "primary_value": rel, "lower_is_better": True}
    schema = {"features": DEMAND_FEATURES, "stats": stats, "window": 72, "horizon": 24,
              "baseline": baseline_histograms(X[m_tr], DEMAND_FEATURES),
              "extra": {"per_window_scaling": "ridership_mean"}}
    return net, metrics, schema, {"n_features": len(DEMAND_FEATURES)}


def train_leak(frames, args, device):
    X, y, groups = ds.build_leak(frames["leak_sensors"])
    m_tr, m_va, m_te = ds.split_by_group(groups, seed=args.seed)
    m_tr = m_tr & (y == 0)          # autoencoder trains on NORMAL data only
    stats = ds.feature_stats_from_tensor(X[m_tr], LEAK_SENSOR_FEATURES)
    Xn = ds.zscore(X, stats, LEAK_SENSOR_FEATURES)

    net = MODEL_CLASSES["leak_autoencoder"]()
    if args.finetune_from:
        _load_finetune("leak_autoencoder", net, args.finetune_from, args.artifacts_dir)
    net.to(device)
    opt = torch.optim.Adam(net.parameters(), lr=args.lr)
    mse = nn.MSELoss()
    rng = np.random.default_rng(args.seed)
    tr_idx = np.where(m_tr)[0]
    for epoch in range(args.epochs):
        net.train()
        rng.shuffle(tr_idx)
        for i in range(0, len(tr_idx), args.batch_size):
            b = tr_idx[i:i + args.batch_size]
            opt.zero_grad()
            loss = mse(net(_t(Xn[b], device)), _t(Xn[b], device))
            loss.backward()
            opt.step()
        log.info("epoch %d train_loss=%.5f", epoch, loss.item())

    net.eval()
    with torch.no_grad():
        err_va = ((net(_t(Xn[m_va], device)) - _t(Xn[m_va], device)) ** 2).mean(1).cpu().numpy()
        err_te = ((net(_t(Xn[m_te], device)) - _t(Xn[m_te], device)) ** 2).mean(1).cpu().numpy()
    normal_va = err_va[y[m_va] == 0]
    threshold = float(np.quantile(normal_va, 0.99)) if len(normal_va) else float(err_va.mean())
    auc = _safe_auc(y[m_te], err_te)
    fpr = float((err_te[y[m_te] == 0] > threshold).mean()) if (y[m_te] == 0).any() else float("nan")
    tpr = float((err_te[y[m_te] == 1] > threshold).mean()) if (y[m_te] == 1).any() else float("nan")
    metrics = {"auc": auc, "threshold": threshold, "fpr_at_threshold": fpr,
               "tpr_at_threshold": tpr, "primary_metric": "auc",
               "primary_value": auc}
    schema = {"features": LEAK_SENSOR_FEATURES, "stats": stats,
              "baseline": baseline_histograms(X[m_tr], LEAK_SENSOR_FEATURES),
              "extra": {"anomaly_threshold": threshold}}
    return net, metrics, schema, {"n_features": len(LEAK_SENSOR_FEATURES)}


def train_gcn(frames, args, device):
    adj, X, delay_y, energy_y, node_names = ds.build_graph(frames["graph"])
    stats = ds.feature_stats_from_tensor(X, GRAPH_NODE_FEATURES)
    Xn = ds.zscore(X, stats, GRAPH_NODE_FEATURES)
    adj_norm = normalize_adjacency(adj).to(device)
    n = len(Xn)
    rng = np.random.default_rng(args.seed)
    perm = rng.permutation(n)
    n_tr = int(0.7 * n)
    n_va = int(0.15 * n)
    idx_tr, idx_va, idx_te = perm[:n_tr], perm[n_tr:n_tr + n_va], perm[n_tr + n_va:]
    mask_tr = torch.zeros(n, dtype=torch.bool, device=device)
    mask_tr[torch.tensor(idx_tr, device=device)] = True

    net = MODEL_CLASSES["fleet_gcn"]()
    if args.finetune_from:
        _load_finetune("fleet_gcn", net, args.finetune_from, args.artifacts_dir)
    net.to(device)
    opt = torch.optim.Adam(net.parameters(), lr=args.lr)
    mse = nn.MSELoss()
    x_t = _t(Xn, device)
    dy_t, ey_t = _t(delay_y, device), _t(energy_y, device)
    for epoch in range(args.epochs):
        net.train()
        opt.zero_grad()
        delay, energy = net(x_t, adj_norm)
        loss = mse(delay[mask_tr], dy_t[mask_tr]) + mse(energy[mask_tr], ey_t[mask_tr])
        loss.backward()
        opt.step()
        log.info("epoch %d train_loss=%.4f", epoch, loss.item())
    net.eval()
    with torch.no_grad():
        delay, energy = net(x_t, adj_norm)
    d_err = (delay - dy_t).cpu().numpy()[idx_te]
    e_err = (energy - ey_t).cpu().numpy()[idx_te]
    metrics = {"mae_delay_min": float(np.abs(d_err).mean()),
               "mae_h2_impact_kg": float(np.abs(e_err).mean()),
               "n_nodes": n, "primary_metric": "mae_delay_min",
               "primary_value": float(np.abs(d_err).mean()), "lower_is_better": True}
    schema = {"features": GRAPH_NODE_FEATURES, "stats": stats,
              "node_names": node_names,
              "baseline": baseline_histograms(X, GRAPH_NODE_FEATURES),
              "extra": {"adjacency": adj.tolist()}}
    return net, metrics, schema, {"n_features": len(GRAPH_NODE_FEATURES)}


def train_carbon(frames, args, device):
    X, y, groups = ds.build_carbon(frames["carbon_periods"])
    m_tr, m_va, m_te = ds.split_by_group(groups, seed=args.seed)
    stats = ds.feature_stats_from_tensor(X[m_tr], CARBON_FEATURES)
    Xn = ds.zscore(X, stats, CARBON_FEATURES)
    y_mean = float(y[m_tr].mean())
    y_std = float(y[m_tr].std() or 1.0)
    Yn = (y - y_mean) / y_std

    net = MODEL_CLASSES["carbon_forecaster"]()
    if args.finetune_from:
        _load_finetune("carbon_forecaster", net, args.finetune_from, args.artifacts_dir)
    net.to(device)
    opt = torch.optim.Adam(net.parameters(), lr=args.lr)
    mse = nn.MSELoss()
    rng = np.random.default_rng(args.seed)
    tr_idx = np.where(m_tr)[0]
    best_val, best_state = float("inf"), None
    for epoch in range(args.epochs):
        net.train()
        rng.shuffle(tr_idx)
        for i in range(0, len(tr_idx), args.batch_size):
            b = tr_idx[i:i + args.batch_size]
            opt.zero_grad()
            loss = mse(net(_t(Xn[b], device)).squeeze(), _t(Yn[b], device))
            loss.backward()
            opt.step()
        net.eval()
        with torch.no_grad():
            val = mse(net(_t(Xn[m_va], device)).squeeze(), _t(Yn[m_va], device)).item()
        log.info("epoch %d val_loss=%.4f", epoch, val)
        if val < best_val:
            best_val, best_state = val, {k: v.clone() for k, v in net.state_dict().items()}
    if best_state:
        net.load_state_dict(best_state)
    net.eval()
    with torch.no_grad():
        pred = net(_t(Xn[m_te], device)).cpu().numpy() * y_std + y_mean
    err = pred - y[m_te]
    metrics = {"mae_kg_co2": float(np.abs(err).mean()),
               "rmse_kg_co2": float(np.sqrt((err ** 2).mean())),
               "rel_mae": float(np.abs(err).mean() / max(y[m_te].mean(), 1e-9)),
               "primary_metric": "rel_mae",
               "primary_value": float(np.abs(err).mean() / max(y[m_te].mean(), 1e-9)),
               "lower_is_better": True}
    schema = {"features": CARBON_FEATURES, "stats": stats,
              "target": {"mean": y_mean, "std": y_std},
              "baseline": baseline_histograms(X[m_tr], CARBON_FEATURES)}
    return net, metrics, schema, {"n_features": len(CARBON_FEATURES)}


TRAINERS = {
    "maintenance_lstm": train_maintenance,
    "demand_forecaster": train_demand,
    "leak_autoencoder": train_leak,
    "fleet_gcn": train_gcn,
    "carbon_forecaster": train_carbon,
}

#: Per-model learning-rate defaults when --lr is not given (tuned on CPU).
DEFAULT_LR = {
    "maintenance_lstm": 1e-3,
    "demand_forecaster": 3e-3,
    "leak_autoencoder": 1e-3,
    "fleet_gcn": 5e-3,
    "carbon_forecaster": 3e-3,
}


# ---------------------------------------------------------------------- main --
def run_one(model: str, frames, args, device: str) -> dict:
    torch.manual_seed(args.seed)
    np.random.seed(args.seed)
    explicit_lr = args.lr
    args.lr = explicit_lr or DEFAULT_LR.get(model, 1e-3)
    mlf = _MLflow(model, args)
    t0 = time.time()
    try:
        net, metrics, schema, model_config = _maybe_ray(
            lambda: TRAINERS[model](frames, args, device), args)
    finally:
        args.lr = explicit_lr
    metrics["train_seconds"] = round(time.time() - t0, 2)
    metrics["source"] = args.source
    metrics["data_note"] = ("SYNTHETIC bootstrap data" if args.source == "synth"
                            else f"source={args.source}")
    n_params = sum(p.numel() for p in net.parameters())
    assert n_params <= 200_000, f"{model} has {n_params} params (>200k budget)"
    metrics["n_params"] = n_params
    version = args.version or new_version()
    out = save_artifact(args.artifacts_dir, model, version, net, model_config,
                        metrics, schema)
    mlf.log_metrics(metrics)
    mlf.log_artifacts(out, model, version)
    log.info("saved %s/%s -> %s (params=%d, %s=%.4f)", model, version, out,
             n_params, metrics["primary_metric"], metrics["primary_value"])
    if args.register:
        reg = read_registry(args.artifacts_dir)
        reg.setdefault("champion", {})[model] = version
        write_registry(args.artifacts_dir, reg)
    return {"model": model, "version": version, "metrics": metrics}


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--model", required=True, choices=MODEL_NAMES + ["all"])
    ap.add_argument("--source", default="synth", choices=["synth", "postgres", "iceberg"])
    ap.add_argument("--data-dir", default=os.environ.get("SYNTH_DATA_DIR", "data/synth_out"))
    ap.add_argument("--database-url", default=os.environ.get("DATABASE_URL", ""))
    ap.add_argument("--lakehouse-dir", default=os.environ.get("LAKEHOUSE_DIR", ""))
    ap.add_argument("--artifacts-dir", default=os.environ.get("MODEL_ARTIFACTS_DIR", "artifacts"))
    ap.add_argument("--epochs", type=int, default=5)
    ap.add_argument("--batch-size", type=int, default=256)
    ap.add_argument("--lr", type=float, default=None,
                    help="override learning rate (per-model tuned defaults otherwise)")
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--days", type=int, default=42, help="synthetic days to generate")
    ap.add_argument("--window", type=int, default=None, help="override window length")
    ap.add_argument("--max-windows", type=int, default=1200)
    ap.add_argument("--device", default="cpu", choices=["cpu", "cuda"])
    ap.add_argument("--ray", action="store_true")
    ap.add_argument("--register", action="store_true")
    ap.add_argument("--finetune-from", default="")
    ap.add_argument("--version", default="")
    args = ap.parse_args()

    logging.basicConfig(level=logging.INFO,
                        format="%(asctime)s %(levelname)s %(name)s %(message)s")
    device = args.device
    if device == "cuda" and not torch.cuda.is_available():
        log.warning("cuda requested but unavailable; falling back to cpu")
        device = "cpu"

    frames = ds.load_frames(args.source, args.data_dir, args.database_url,
                            args.lakehouse_dir, days=args.days, seed=args.seed)
    models = MODEL_NAMES if args.model == "all" else [args.model]
    results = [run_one(m, frames, args, device) for m in models]
    print(json.dumps(results, indent=2))


if __name__ == "__main__":
    main()
