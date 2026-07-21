#!/usr/bin/env python3
"""
Train the frozen v0 fraud-scoring model and export it as a pure-weights artifact.

Runs ONCE. No tuning, no cross-validation, no plotting: the model is a frozen
placeholder. The StandardScaler is folded algebraically into the
exported weights, so the Go inference service does a raw dot-product + sigmoid on
UNSCALED features, with no scaling step and no ML runtime.
"""

import json
import datetime as dt
from pathlib import Path

import numpy as np
import pandas as pd
from scipy.special import expit as sigmoid          # numerically stable 1/(1+e^-z)
from sklearn.linear_model import LogisticRegression
from sklearn.preprocessing import StandardScaler

# --- paths (train.py lives in training/, so parent.parent is the repo root) ---
ROOT = Path(__file__).resolve().parent.parent
DATA = ROOT / "data" / "creditcard.csv"
OUT_DIR = ROOT / "models" / "v1.0.0"
OUT_FILE = OUT_DIR / "model.json"
MODEL_VERSION = "v1.0.0"

# --- feature order: Time, V1..V28, Amount (30 features). THIS ORDER IS THE CONTRACT. ---
# The Go service must feed features in exactly this order. Do not reorder.
FEATURES = ["Time"] + [f"V{i}" for i in range(1, 29)] + ["Amount"]
assert len(FEATURES) == 30

# --- load ---
df = pd.read_csv(DATA)
X = df[FEATURES].to_numpy(dtype=np.float64)
y = df["Class"].to_numpy(dtype=np.int64)
print(f"Loaded {len(df):,} rows ({int(y.sum())} fraud)")

# --- scale, then train in scaled space ---
scaler = StandardScaler().fit(X)
Xs = scaler.transform(X)
# class_weight='balanced' is the ONE knob. With ~0.17% fraud, an unweighted model
# collapses every score toward 0 and /score becomes untestable. This is a legibility
# choice, not tuning: no grid, no CV, no metric optimization. max_iter is just "let it
# finish", not a hyperparameter.
clf = LogisticRegression(class_weight="balanced", max_iter=1000).fit(Xs, y)

mu = scaler.mean_          # (30,) per-feature mean
sigma = scaler.scale_      # (30,) per-feature std actually used by the transform
w = clf.coef_[0]           # (30,) weights in SCALED space
b = clf.intercept_[0]      # scalar in SCALED space

# --- fold the scaler into the weights ---
#   logit = Σ wᵢ·zᵢ + b ,  zᵢ = (xᵢ - μᵢ)/σᵢ
#         = Σ (wᵢ/σᵢ)·xᵢ + (b - Σ wᵢ·μᵢ/σᵢ)
w_folded = w / sigma
b_folded = float(b - np.sum(w * mu / sigma))

# --- verify the fold: folded path on RAW features == sklearn on scaled features ---
sample = X[:2000]
proba_sklearn = clf.predict_proba(scaler.transform(sample))[:, 1]
proba_folded = sigmoid(sample @ w_folded + b_folded)
max_diff = float(np.max(np.abs(proba_sklearn - proba_folded)))
assert max_diff < 1e-9, f"FOLD MISMATCH: {max_diff}"
print(f"Fold verified: max |Δ| over 2000 rows = {max_diff:.2e}")

# --- write the artifact (this is the whole contract with the Go service) ---
OUT_DIR.mkdir(parents=True, exist_ok=True)
artifact = {
    "model_version": MODEL_VERSION,
    "created_at": dt.date.today().isoformat(),
    "feature_order": FEATURES,
    "weights": w_folded.tolist(),
    "intercept": b_folded,
}
OUT_FILE.write_text(json.dumps(artifact, indent=2))
print(f"Wrote {OUT_FILE.relative_to(ROOT)}")

# --- golden vectors: hardcode these into a Go test to prove parity with Python ---
legit_i = int(np.where(y == 0)[0][0])
fraud_i = int(np.where(y == 1)[0][0])
for label, i in (("legit", legit_i), ("fraud", fraud_i)):
    score = float(sigmoid(X[i] @ w_folded + b_folded))
    print(f"\ngolden[{label}] row={i} expected_score={score:.6f}")
    print("  features =", json.dumps([float(v) for v in X[i]]))