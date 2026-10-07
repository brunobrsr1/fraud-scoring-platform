# Fraud Scoring Platform

A real-time fraud-scoring inference service backed by a hand-written Raft consensus
core that makes model-version promotions **ordered, durable, and observable**. The
scoring model is a frozen placeholder — the infrastructure is the point.

> **Status:** v0 — local single-node serving (in progress). The distributed registry
> and Raft core are the target, not yet built. See [Roadmap](#roadmap).

---

## The problem

A fraud-scoring service decides, at the instant a payment is attempted, whether it looks
legitimate. The engineers who own the scoring model need one thing above all when they
change which model is live — especially on a **rollback** mid-incident: the acknowledgement
they get back has to be worth something.

"Worth something" means two guarantees, and neither is speed:

- **Order** — if two engineers act at the same instant, exactly one goes first, and there
  is never more than one authoritative answer to *what is live*.
- **Durability** — once acknowledged, the decision survives the loss of the machine that
  accepted it.

A stateless API over a quorum-committed database gives order and durability. Where it falls
short is **failover**: deciding *which replica leads* after the primary dies is itself the
consensus problem, one layer down. The obvious design doesn't remove consensus — it
relocates it, and pays someone else to have written it. This project writes that core, on
purpose.

When a majority of the registry is unreachable, the system **refuses new promotions** rather
than accepting one it might lose. Promotions are rare, deliberate, human-initiated events; a
promotion delayed by thirty seconds is recoverable, while one silently lost is not.

## What makes it interesting

Three decisions carry the project. Each is recorded as an ADR.

- **Commit ≠ convergence.** The promotion API returns *both*, as separate events.
  *Committed* means the decision is safe and ordered — quorum has persisted it, power loss
  can't undo it. *Converged* means the fleet is actually serving it. Consensus guarantees the
  first and says nothing about the second — and collapsing them into one green check is
  exactly what lets an engineer stop watching while broken traffic is still being scored.

- **Fail-open on the request path; self-eject on the routing path.** A node that can't reach
  the registry keeps scoring with its last known model (a model accurate ten seconds ago is
  accurate now) and exposes its degradation — it never refuses. But when time-since-contact
  crosses a threshold, the node reports *not-ready* and the load balancer drains it
  automatically. The human's job ends at commit; the fleet handles the rest.

- **`seconds_since_registry_contact`, not `staleness_seconds`.** A node cannot prove it is out
  of date — it can only prove it cannot prove it is current. The honest measurement is time
  since last contact; the caller, who knows what is at stake, interprets it.

## Architecture

```
[ clients ] ── HTTP ──▶ [ ALB ] ──▶ [ inference nodes ]   (stateless, cached model)
                                          │
                                          │ read current model version
                                          ▼
                              [ Raft registry — 3 nodes ]
                                leader + 2 followers
                                (model / config registry only)
```

<!-- TODO: replace with a real diagram — boxes, arrows, protocols on the arrows. -->

- **Inference nodes** score requests against an in-memory cached model. Stateless; scale
  horizontally behind the ALB.
- **Raft registry** — a static 3-node group, the single source of truth for *which model
  version is live*. Rare, human-initiated writes; single group, no sharding.
- **Deployment** — production runs on AWS: EC2 + Docker + ALB + Terraform. Kubernetes is a
  **local-dev convenience only**, not an architectural pillar.

## API contract

### `POST /v1/score`

Request:
```json
{
  "transaction_id": "tx_987654321alpha",
  "features": {
    "Time": 0.0,
    "V1": -1.359807,
    "V2": -0.072781,
    "...": "... V3 through V27 ...",
    "V28": -0.021053,
    "Amount": 149.62
  }
}
```

Response (v0):
```json
{
  "transaction_id": "tx_987654321alpha",
  "score": 0.26201699054165745,
  "meta": {
    "model_version": "v1.0.0"
  }
}
```

The features above are the `golden[legit]` row of the dataset, truncated here for readability;
the full-precision vector and the exact score it must produce are asserted in
[`internal/model/model_test.go`](internal/model/model_test.go), and end to end over HTTP in
[`internal/httpapi/score_test.go`](internal/httpapi/score_test.go).

- `features` — a **named** map, not a positional array. The server builds the model input from
  the artifact's own `feature_order`, so a client cannot silently mis-order the vector. Every
  feature must be present exactly once, and unknown keys are rejected: a missing or extra key
  is a `400`, never a plausible-but-wrong score. The feature schema is immutable across model
  versions — a promotion may change weights, never the feature set ([ADR-004](docs/adr/adr-004.md)).
- `score` — a `float64` strictly in `[0.0, 1.0]`, returned at full precision and never rounded.
  The service returns a **probability, never a verdict**; thresholding is the caller's business
  logic.
- `meta.model_version` — the model that produced this score.

**Planned with the registry.** Two more `meta` fields arrive once nodes read the live model from
the Raft registry:

- `registry_sync` ∈ `{ fresh, stale, stale_critical }`.
- `seconds_since_registry_contact` — deliberately *not* `staleness_seconds` (see above).

v0 has no registry, so it omits them rather than report `fresh` / `0`: that would claim a
contact that never happened.

#### Validation

Strict on purpose: a request the service is unsure about is rejected, never guessed at.

- `Content-Type` must be `application/json`. The body is at most 64 KiB and holds exactly one
  JSON object.
- Field names match exactly (case-sensitive), and a key may not appear twice at any level.
  Go's `encoding/json` would otherwise accept `Transaction_ID` and keep the last of two
  duplicate keys, so the body gets a token pass before it is decoded.
- `transaction_id` is a required string of at most 128 bytes. `features` is a required object.
- Each feature is a JSON number that fits in a `float64`. Features that are each finite but
  together overflow the model are a `400`, not a `500`: the bad input is the client's.

#### Errors

Errors from `/v1/score` are `{"error": "<message>"}` with `Content-Type: application/json`.

| Status | When |
|---|---|
| `400` | Malformed, empty or truncated JSON; duplicate or unknown keys; a missing or invalid field or feature |
| `404` | Unknown path |
| `405` | Wrong method, e.g. `GET /v1/score` |
| `413` | Body over 64 KiB |
| `415` | `Content-Type` is not `application/json` |
| `500` | A server-side failure; never caused by request content |

`404` and `405` currently come from Go's router as `text/plain`, not the JSON error shape.

### `GET /healthz` and `GET /readyz`

- `/healthz` — liveness only: `200 {"status":"ok"}` while the process can answer. It checks no
  dependency on purpose: a liveness probe that fails on a registry outage would restart the
  whole fleet at once.
- `/readyz` — readiness: `200 {"status":"ready"}` when a model is loaded, else
  `503 {"status":"not_ready"}`. In v0 the model loads at startup or the process exits, so a
  running node is always ready. With the registry, this is where self-eject lands: past a
  time-since-contact threshold (OQ-5), the node reports not-ready and the load balancer drains it.

<!-- TODO: /metrics, client-facing timeout behaviour. -->

## The model

A frozen logistic-regression classifier trained **once** on the public
[Kaggle Credit Card Fraud dataset](https://www.kaggle.com/datasets/mlg-ulb/creditcardfraud)
(features `Time, V1..V28, Amount`, already PCA-anonymized). Exported as a plain JSON file of
weights + intercept; the Go service parses it on startup and scores with a raw dot-product
followed by a sigmoid — **no ML runtime, no scaling step at inference, a pure statically-linked
binary**. The `StandardScaler` is folded algebraically into the exported weights so inference
operates directly on raw features.

The model is deliberately not the subject of study: no retraining, no drift adaptation, no
hyperparameter tuning. The object of study is infrastructure behaviour during state changes.

## Non-goals

Explicitly out of scope — each with an accepted cost documented in the design spec: model
training / retraining, multi-tenancy / auth / UI, sharding, dynamic cluster membership,
canary or blue-green rollout, and business-level verdicts. The system makes **one** guarantee
well — that a promotion is ordered, durable, and observable — and cuts everything that
competes with it for attention.

## Design decisions (ADRs)

Written:

- [**ADR-001** — Raft implementation](docs/adr/adr-001.md) — write the consensus core from the
  Raft paper instead of importing etcd/Consul, kept independent of fraud scoring, accepting the
  higher correctness risk in exchange for making consensus the thing being studied.
- [**ADR-002** — Production deployment target](docs/adr/adr-002.md) — EC2 + Docker + ALB +
  Terraform on AWS; Kubernetes for local development only, never a production dependency.
- [**ADR-004** — Scoring API contract](docs/adr/adr-004.md) — named feature map over a positional
  array, strict validation in both directions (duplicate keys included), and a feature schema
  held immutable across model versions so that nodes mid-convergence cannot accept different
  request shapes.

Decided, write-up pending:

- **ADR-003** — Registry read semantics: linearizable leader reads vs. local follower reads.

<!-- TODO: write up ADR-003 (half a page) and link it here. -->

## Service-level objectives

Committed *before* implementation and reported here honestly — including failures. Numbers
picked after measuring are excuses, not objectives.

| SLO | Target |
|---|---|
| p99 scoring latency (nominal load) | < 50 ms |
| Sustained throughput | ≥ 200 req/s |
| Leader election after leader death | < 2 s |
| Committed writes lost on leader failure | 0 |
| AWS cost budget | < €20 / month |

### Measurement

- **Nominal load:** 100 req/s sustained scoring requests using the versioned
  golden request payload.
- **Latency:** end-to-end HTTP request latency, measured at the client.
- **Throughput:** successful scoring requests per second sustained for 10 minutes.
- **Leader election:** time from leader failure until a new leader is elected and able to accept writes.
- **Committed write loss:** number of previously committed writes missing after leader failure and recovery.
- **AWS cost:** monthly infrastructure cost measured from AWS billing data.

## Running locally

v0 is a single Go binary serving the frozen model. The quickest path needs only Docker:

```bash
docker compose up --build          # listens on :8080
curl -s localhost:8080/readyz      # {"status":"ready"}

# examples/golden-fraud.json is the golden fraud row used by the parity tests
curl -s -X POST localhost:8080/v1/score \
  -H 'Content-Type: application/json' -d @examples/golden-fraud.json
# {"transaction_id":"golden-fraud","score":0.9999998777523079,"meta":{"model_version":"v1.0.0"}}
```

Without Docker (Go 1.26+), run from the repository root, since the default model path is
relative:

```bash
go run ./cmd/score-server
go test ./...
```

| Variable | Default | Meaning |
|---|---|---|
| `HTTP_ADDR` | `:8080` | Listen address (`host:port`; port `0` lets the kernel pick) |
| `MODEL_PATH` | `models/v1.0.0/model.json` | Model artifact, relative to the working directory |

On `SIGINT` / `SIGTERM` the process stops accepting connections and gives in-flight requests up
to 5 s to finish.

<!-- Target: docker compose up — 3 Raft nodes + inference service + Prometheus/Grafana. -->

## On the Raft implementation

The consensus core is written from the Raft paper, independently — **not** ported from
coursework. The MIT 6.5840 chaos-test harness is used *only* to validate the implementation,
never as its structure. Honest provenance is the point: the value is in having built and
debugged the core, and this section stays accurate to that.

## Roadmap

- [x] Architecture spec — decided sections (§1.1–§1.7)
- [ ] **v0 — local single-node serving** (frozen model, `POST /v1/score`) ← *now*
- [ ] Reading block (Raft paper, DDIA ch. 9) → Tier-2 decisions + ADRs
- [ ] Raft core (from paper) + registry state machine
- [ ] AWS deployment (Terraform, 3-node cluster across 2 AZs)
- [ ] Load testing + SLO verification

---

*The full design spec and decision log are maintained separately; this README is the
10-minute overview.*