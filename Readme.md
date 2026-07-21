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
  "user_id": "usr_102938",
  "amount": 250.00,
  "currency": "EUR",
  "merchant_category_code": "5411",
  "timestamp": "2026-07-16T15:55:10Z"
}
```

Response:
```json
{
  "transaction_id": "tx_987654321alpha",
  "score": 0.04,
  "meta": {
    "model_version": "v2.1.0",
    "registry_sync": "fresh",
    "seconds_since_registry_contact": 0
  }
}
```

- `score` — a `float64` strictly in `[0.0, 1.0]`. The service returns a **probability, never a
  verdict**; thresholding is the caller's business logic.
- `registry_sync` ∈ `{ fresh, stale, stale_critical }`.
- `seconds_since_registry_contact` — deliberately *not* `staleness_seconds` (see above).

<!-- TODO: /healthz (liveness), /readyz (must encode staleness for self-eject), /metrics,
     error codes, timeout behaviour. -->

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

- **ADR-001** — Write the Raft core vs. import etcd/Consul.
- **ADR-002** — Deployment target: EC2 + Docker + ALB + Terraform; Kubernetes local-dev only.
- **ADR-003** — Registry read semantics: linearizable leader reads vs. local follower reads.

<!-- TODO: write ADRs (half a page each) and link them here. -->

## Service-level objectives

Committed *before* implementation and reported here honestly — including failures. Numbers
picked after measuring are excuses, not objectives.

| SLO | Target |
|---|---|
| p99 scoring latency (nominal load) | _TBD_ |
| Sustained throughput | _TBD_ |
| Leader election after leader death | _< TBD s_ |
| Committed writes lost on leader failure | **0** (to be proven) |
| AWS cost budget | _< €TBD / month_ |

<!-- Fill after the 5h reading block. -->

## Running locally

```bash
# TODO — once the service skeleton exists:
docker compose up
```

<!-- Target: 3 Raft nodes + inference service + Prometheus/Grafana in docker-compose. -->

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