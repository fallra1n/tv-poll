# TV Poll — anonymous voting backend

Backend for a national TV polling company: a ~60s TV spot shows a one-question
poll, viewers scan a QR code / follow a link and vote anonymously — no
registration. Built for ~100M potential viewers per spot with a short,
foreknown peak (the broadcast schedule is known in advance).

This is a take-home assignment. The full architecture rationale — load model,
stack choices, deduplication design, data model, API contract, and every
rejected alternative — lives in [`docs/ai/`](docs/ai/README.md), including
dead ends and revisions made while implementing (`docs/ai/sessions/`,
`docs/ai/what-ai-got-wrong.md`). This file covers only what's needed to run
and test the service.

## Architecture, in one paragraph

Write-heavy, short predictable peak, aggregated-only reads. The vote hot path
touches Redis exactly once (`SET NX GET` on a dedup key derived from a
stateless, self-signed HMAC token — no Lua, correctness comes from Redis's
own atomicity) and never touches Postgres. Vote counts live in memory per
instance, flushed to Redis once a second; a leader-elected snapshotter drains
Redis into Postgres once a second, which doubles as the results history.
Postgres holds poll definitions, FSM state, and snapshots — never individual
votes. See [`docs/ai/02-load-model.md`](docs/ai/02-load-model.md) for the load
math this is built from and [`docs/ai/01-stack.md`](docs/ai/01-stack.md) for
why each component was picked.

## Prerequisites

- Docker + Docker Compose
- `curl`, `python3` (for the examples below — any JSON-capable client works)
- Go 1.23+ (only if running `go test`/`go build` outside Docker)
- [k6](https://k6.io/) (only for load testing)

## Run it

```bash
make up      # builds the image, starts postgres+redis, runs migrations, starts the API
make down    # stops everything and removes volumes
make logs    # follow the API's logs
```

`make up` polls `GET /healthz` implicitly via each service's own healthcheck;
once it returns, confirm directly:

```bash
curl -s http://localhost:8080/healthz
# {"postgres":"ok","redis":"ok","status":"ok"}
```

Config is env vars with sane local defaults — see
[`.env.example`](.env.example) for the full list and
[`docker-compose.yml`](docker-compose.yml) for what's actually passed to the
container. Nothing needs to be set to run locally; `ADMIN_TOKEN` and
`VOTE_TOKEN_SECRET` should be changed before deploying anywhere real.

## Walking through the full flow

Everything below is real `curl` against a running `make up` stack — not
illustrative pseudocode.

```bash
BASE=http://localhost:8080
ADMIN=local-dev-admin-token   # matches docker-compose.yml's default

# 1. Create a poll (draft)
POLL=$(curl -s -XPOST $BASE/v1/admin/polls -H "Authorization: Bearer $ADMIN" \
  -H 'Content-Type: application/json' \
  -d '{"question":"Кто выиграет?","options":[{"label":"Команда А"},{"label":"Команда Б"}]}' \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')

# 2. Schedule it — closes_at is fixed the moment this is called
#    (scheduled_at + voting_window_seconds), not when it actually opens
FUTURE=$(python3 -c 'import datetime;print((datetime.datetime.utcnow()+datetime.timedelta(seconds=5)).strftime("%Y-%m-%dT%H:%M:%SZ"))')
curl -s -XPOST $BASE/v1/admin/polls/$POLL/transitions -H "Authorization: Bearer $ADMIN" \
  -H 'Content-Type: application/json' -d "{\"to\":\"scheduled\",\"scheduled_at\":\"$FUTURE\"}"

# 3. Wait — nobody needs to click "open": the poll opens itself at
#    scheduled_at (an elected leader instance transitions it automatically)
sleep 6

# 4. Public, CDN-cacheable poll definition (no auth, no state field —
#    see docs/ai/05-api-contract.md for why)
curl -s -i $BASE/v1/polls/$POLL

# 5. Get a vote token (two-step flow: token first, then vote)
TOKEN=$(curl -s -XPOST $BASE/v1/polls/$POLL/token | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')

# 6. Vote
curl -s -XPOST $BASE/v1/polls/$POLL/votes -H "X-Vote-Token: $TOKEN" \
  -H 'Content-Type: application/json' -d '{"option_id":1}'
# {"status":"accepted","option_id":1}

# 7. Same token again — rejected, and told which option was already counted
curl -s -XPOST $BASE/v1/polls/$POLL/votes -H "X-Vote-Token: $TOKEN" \
  -H 'Content-Type: application/json' -d '{"option_id":2}'
# {"status":"rejected","reason":"duplicate","option_id":1}

# 8. Results (admin) — eventually consistent, up to ~1s behind
sleep 2
curl -s $BASE/v1/admin/polls/$POLL/results -H "Authorization: Bearer $ADMIN"

# 9. Votes-over-time (the snapshot history, for a chart)
curl -s $BASE/v1/admin/polls/$POLL/results/timeseries -H "Authorization: Bearer $ADMIN"

# 10. Close it
curl -s -XPOST $BASE/v1/admin/polls/$POLL/transitions -H "Authorization: Bearer $ADMIN" \
  -H 'Content-Type: application/json' -d '{"to":"closed"}'
```

The full wire contract is [`api/openapi.yaml`](api/openapi.yaml) (lint it with
`npx @redocly/cli lint api/openapi.yaml`); the rationale for every
non-obvious call in it is in
[`docs/ai/05-api-contract.md`](docs/ai/05-api-contract.md).

## Metrics

Prometheus metrics are on a **separate, loopback-only listener**
(`METRICS_ADDR`, default `127.0.0.1:9090`), not the main port — an
unauthenticated `votes_accepted_total{poll_id=...}` on the same port as the
vote endpoints would leak a live poll's tally before an admin publishes
results.

```bash
curl -s http://localhost:9090/metrics | grep -E '^(votes_|vote_handler_duration|redis_command_duration|inflight_requests)'
```

## Testing

```bash
cd backend
go test ./... -short   # unit tests only, no Docker needed, ~2s
go test ./...          # + integration tests against real Postgres/Redis via testcontainers, needs Docker
```

Unit tests cover pure logic: poll validation, the FSM's transition table,
vote-token sign/verify (including secret rotation and every malformed-input
case), the local rate limiter, the in-memory counter's swap/restore
semantics, and the snapshot monotonicity merge. Integration tests run
against real Postgres 16 / Redis 7 containers (deliberately not
`miniredis`, which emulates the exact command combination the dedup claim
relies on — `SET NX GET` — incompletely) and include the two properties
that matter most for correctness:

- **Concurrency**: `TestDedupStore_TryClaim_Concurrent` fires 200 goroutines
  claiming the same token concurrently — exactly one is accepted, proving
  the dedup guarantee holds under a real race, not just in isolation.
- **Monotonicity**: `TestSnapshotStore_InsertSnapshot_Monotonicity` proves
  that if Redis loses data between two snapshot ticks, the stored result
  never goes backwards (a votes-over-time chart must never show votes
  disappearing).

## Load testing

```bash
# Every k6 VU on one machine shares one source IP, and the per-(poll,ip)
# rate limiter is designed to stop exactly that pattern — so it has to be
# raised for the duration of a load test, or you're measuring the limiter,
# not the vote hot path.
docker compose down -v
RATE_LIMIT_RPS=100000 RATE_LIMIT_BURST=100000 docker compose up --build -d

k6 run load/vote.js                    # default: 3,000 votes over the full curve
TARGET_VOTES=1200000 k6 run load/vote.js  # scale the whole curve up
```

`load/vote.js` uses k6's `ramping-arrival-rate` executor (not a flat VU
count — voting is a two-step flow, fetch a token then spend it, which is
why `vegeta`/`wrk` don't fit) shaped to the arrival-time curve from
[`docs/ai/02-load-model.md` §3](docs/ai/02-load-model.md): a fast rise in
the first 15s, a plateau at 30–60s while the spot is still airing, then a
long tail after it ends.

### Measured results

Single API instance, `docker compose` (Go container + Postgres 16 +
Redis 7), on a MacBook — **not** the dedicated 4-vCPU box
`02-load-model.md` §4.2 estimates against, and the whole docker-compose
network stack is included in these numbers, not a bare Go binary. These are
real measurements, not the extrapolated planning numbers from the docs
presented as if they were measured — see that document's own explicit
warning about the difference.

**The actual deliverable run** — `load/vote.js` unmodified, shaped to the
full §3 curve (0→peak in 30s, plateau, 120s tail), `TARGET_VOTES=1200000`
(peak ≈ 12,000 votes/s):

| | |
|---|---|
| Duration | 5m3s (the curve's own length) |
| Votes accepted | 1,604,210 |
| HTTP errors | 0 (0.00% of 3,208,429 requests) |
| p95 / **p99** vote latency | 73.6 ms / **98.6 ms** |
| SLO (`p99 < 200ms`) | ✓ passed |

Zero failures across the full arrival curve, not just at a single sustained
rate — the shape (fast rise → plateau → long tail) doesn't itself create
latency a flat load at the same intensity wouldn't.

**Finding the ceiling** — a series of shorter constant-rate probes (ad hoc,
not the committed script, same rate-limiter caveat as above) to bracket
where p99 crosses 200ms:

| Target rate (votes/s) | Achieved | p95 | p99 | Errors |
|---|---|---|---|---|
| 500 | 500 | 0.9 ms | — | 0% |
| 3,000 | 2,663 | 0.4 ms | — | 0% |
| 10,000 | 8,875 | 1.5 ms | — | 0% |
| 12,000 | 10,893 | 21 ms | 45 ms | 0% |
| 14,000 | 12,625 | 40 ms | 70 ms | 0% |
| 15,000 | 13,474 | 29 ms | — | 0% |
| 17,000 | 8,714 | 526 ms | 786 ms | 0%* |

\* 0% HTTP failures, but throughput collapsed below target and
`dropped_iterations` climbed — the pipeline is saturated, not returning
errors.

**Single-instance ceiling (p99 < 200ms SLO): ~12,000–14,000 votes/s** on this
laptop. `02-load-model.md` §4.2's untested planning estimate was 15,000
RPS/instance on dedicated hardware — close to what was actually measured
here despite the docker-compose overhead, which is a reasonable validation
of that estimate rather than a contradiction of it.

**Extrapolation to the base scenario's 75,000 RPS peak** (§4.2): linear,
stated explicitly rather than re-measured — a laptop cannot generate 75k
RPS to test that directly. `75,000 / 13,000 ≈ 6` instances at the measured
ceiling, `× 1.5` headroom (the same margin the docs apply everywhere) `≈ 9`
instances — in the same range as the original 8-instance estimate.

## Frontend

`frontend/` is a separate Bun/React 19 codebase, built only against
[`api/openapi.yaml`](api/openapi.yaml) — see
[`docs/ai/06-frontend.md`](docs/ai/06-frontend.md) for the architecture
rationale and [`frontend/README.md`](frontend/README.md) for the full command
list.

```bash
cd frontend
bun install
bun run api:types   # generate src/lib/api/schema.generated.ts from ../api/openapi.yaml
bun run dev          # http://localhost:3000, with the backend from `make up` at :8080
```

Two HTML entrypoints, built and cached independently:

- `/polls/{pollId}` — the public voting page. No router, no admin code, no
  Recharts in this bundle — this is the page up to ~28M devices could load
  per broadcast (`docs/ai/02-load-model.md`), so it stays minimal.
- `/admin/*` — poll management, FSM transitions, and results/timeseries
  charts, Bearer-token gated.

**CDN boundary**: `frontend/dist/` is meant to be served from a static
CDN/edge, never from this Go service — only `POST` votes and the two GETs
that need CORS hit `backend/` directly. See `frontend/README.md`'s CDN
section for the exact fallback rewrite rules and cache headers a real deploy
needs.

## Project layout

```
backend/          Go service (see backend/internal for package-level docs)
api/openapi.yaml   Wire contract — the boundary with the frontend
frontend/          Separate codebase, built against api/openapi.yaml
docs/ai/           Architecture rationale, load model, decision record, AI session logs
load/vote.js       k6 load test
```

## What's deliberately not here

No Kubernetes manifests, no CI beyond what's implied by `go test`, no
production secret management, no CAPTCHA/proof-of-work on voting (would
directly contradict R2: dedup only needs to stop an average non-technical
user, not a proxy pool), no per-vote storage (results are aggregated only,
by design — R8). Each of these is a deliberate decision, not an omission;
see the "что сознательно не берём" tables in `docs/ai/01-stack.md` and
`docs/ai/03-deduplication.md`.
