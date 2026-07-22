#!/usr/bin/env python3
"""
Extract golden vectors from the FROZEN artifact, for the Go parity test.

Read-only by design: this loads the already-exported models/v1.0.0/model.json
(it does NOT retrain and does NOT rewrite the artifact) and scores two reference
rows from the dataset with those exact weights. That anchors the golden numbers
to the very file the Go service loads, so a green TestGoldenParity proves Go and
Python agree to <1e-9 on the shipped model.

Run (fish):
  cd training && source .venv/bin/activate.fish && python golden.py

Then paste the printed feature vectors + expected scores into
internal/model/model_test.go.
"""

import json
from pathlib import Path

import numpy as np
import pandas as pd
from scipy.special import expit as sigmoid  # numerically stable 1/(1+e^-z)

ROOT = Path(__file__).resolve().parent.parent
DATA = ROOT / "data" / "creditcard.csv"
MODEL_FILE = ROOT / "models" / "v1.0.0" / "model.json"

# --- load the frozen artifact (source of truth; never modified here) ---
artifact = json.loads(MODEL_FILE.read_text())
features = artifact["feature_order"]          # 30 names: Time, V1..V28, Amount
w = np.asarray(artifact["weights"], dtype=np.float64)
b = float(artifact["intercept"])
assert len(features) == len(w) == 30, "unexpected artifact shape"

# --- load the same rows train.py uses for its golden vectors ---
df = pd.read_csv(DATA)
X = df[features].to_numpy(dtype=np.float64)    # in feature_order
y = df["Class"].to_numpy(dtype=np.int64)

legit_i = int(np.where(y == 0)[0][0])          # first legit row
fraud_i = int(np.where(y == 1)[0][0])          # first fraud row

print(f"Using {MODEL_FILE.relative_to(ROOT)} (version {artifact['model_version']})")
for label, i in (("legit", legit_i), ("fraud", fraud_i)):
    score = float(sigmoid(X[i] @ w + b))
    # High precision so the Go side can assert parity to <1e-9.
    print(f"\ngolden[{label}] row={i} expected_score={score:.17g}")
    print("  features =", json.dumps([float(v) for v in X[i]]))
