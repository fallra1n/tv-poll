# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

This repository is in the **planning stage**. `backend/` and `frontend/` are
empty directories — no code has been written yet. The only substantive
content is the architecture/decision record under `docs/ai/`. Do not assume
any build/lint/test tooling exists until it's actually added; check the
directories before running commands.

## What this is

A take-home backend assignment for a national TV polling company: viewers
watch a ~60s TV spot showing a one-question poll (multiple choice / A-B /
etc.), scan a QR code / follow a link, and vote anonymously — no
registration. The service must support anonymous voting, poll creation
(admin), and viewing anonymized results (admin). Target scale is ~100M
potential viewers per spot, with a ~1 minute voting window per broadcast.
The full task text (verbatim, in Russian) is in `docs/ai/00-task-original.md`.

All docs under `docs/ai/` are required deliverables (the assignment mandates
that AI-collaboration artifacts ship in the repo, including dead ends and
revised estimates — not just polished conclusions). Read `docs/ai/README.md`
first; it explains the numbering and reading order. **Read order for
understanding the reasoning is reverse of file numbering**: start with
`02-load-model.md` (load model — the foundation), then `01-stack.md` (stack
choices, which follow from the load model), then `00-task-original.md` for
the source requirements. Docs are written in Russian.

When adding new architecture decisions, follow the existing pattern: put
numbers/assumptions in a dedicated doc, mark assumptions explicitly, and
record rejected alternatives under a "what we deliberately don't take"
section rather than silently omitting them.

## Planned architecture (from docs/ai/01-stack.md and 02-load-model.md)

The system is **write-heavy with a short, predictable, foreknown peak**
(broadcast schedule is known in advance) and **aggregated-only reads** —
individual votes are never persisted. This drives every major decision below.

- **Language/HTTP**: Go 1.23+, stdlib `net/http` (Go 1.22+ method+path-param
  routing) + `go-chi/chi/v5` for middleware chaining and route grouping only.
- **Hot path (voting) touches Redis only, never PostgreSQL.** Accepting a
  vote is a single atomic Lua script: `SET dedup_key NX EX ttl` guarding a
  `HINCRBY` on the counter — dedup check and increment happen in one
  round-trip with no race window.
- **Counter sharding**: exactly one poll is live during a broadcast, so its
  counter would be a single Redis hot key/shard. Counters are artificially
  sharded as `poll:{id}:counts:{0..N-1}` (N ≈ 64), incremented into a random
  shard, summed across shards on read. Dedup keys shard themselves since
  tokens are random.
- **PostgreSQL is cold storage only**: poll definitions, voting-window
  state (`draft → scheduled → open → closed`), periodic counter snapshots
  (also produces the votes-over-time chart for the admin UI), and admin
  audit log. Load on it is trivially low (a handful of admin users, one
  snapshot write per second) — explicitly not a bottleneck, not tuned.
- **Redis is not the source of truth beyond ~1s**: a worker snapshots
  counters into Postgres periodically; a Redis crash loses at most one
  snapshot interval's worth of votes.
- **Driver/tooling**: `jackc/pgx/v5` (pgxpool) with hand-written SQL, no
  ORM/codegen (schema is ~4-5 tables); `redis/go-redis/v9`;
  `pressly/goose/v3` migrations; `caarlos0/env/v11` + `.env` for config;
  `log/slog` with JSON handler (hot path logs nothing but errors —
  100k votes/sec of log lines would be its own outage); `prometheus/client_golang`
  for metrics (`votes_accepted_total`, `votes_rejected_total{reason}`,
  vote-handler latency histogram, `redis_command_duration_seconds`,
  `inflight_requests`).
- **Admin auth**: single bearer token from env, compared with
  `subtle.ConstantTimeCompare`. Public voting endpoints and admin endpoints
  are on separate routers with separate middleware (no auth + rate limit vs.
  auth + no rate limit).
- **Voting token delivery**: dual — server sets both a cookie and returns the
  token in the response body; frontend also persists it to `localStorage` and
  sends it as `X-Vote-Token`. Server accepts either source. This is a
  deliberate response to third-party-cookie blocking on TV-scale mobile
  audiences, and is calibrated to R2 (basic, not adversarial, dedup).
- **Static/CDN boundary**: poll definitions are immutable once published and
  are meant to be served from a CDN with aggressive `Cache-Control`, never
  from this backend. Only the vote `POST` hits the Go service. This is the
  single most load-bearing architectural decision — see the Coinbase Super
  Bowl QR-code case study in `02-load-model.md` §2 for why (page-serving
  collapsed, not vote intake).
- **Voting window is wider than the ad**: default 5 minutes even though the
  spot is ~60s, because ~35% of votes arrive after the spot ends (see the
  arrival-time distribution model in `02-load-model.md` §3).
- **Pre-scaling by broadcast schedule, not reactive autoscaling**: the peak
  is known to the second and lasts ~45s, shorter than a reactive autoscaler's
  reaction time. Planned `/internal/warmup` endpoint + a `scheduled_at` field
  on polls to pre-warm connection pools and `SCRIPT LOAD` the Lua script
  ahead of air time.
- **Degradation ladder** (`02-load-model.md` §6.7): normal → `429` with
  jittered `Retry-After` → optional sampled acceptance (1-in-K, explicitly
  off by default and logged when enabled, since undercounting is a
  reputational risk for a polling company) → Redis down (stop accepting
  votes, keep serving results).
- **Dedup/anonymity tradeoff**: any signal used to recognize a repeat voter
  is by definition an identifier, which sits in tension with anonymity —
  this is called out explicitly as R13 in `00-task-original.md` rather than
  hand-waved. Corollary (R14/§6.8): false-positive blocking (rejecting a
  real viewer) is treated as strictly worse than missed ballot-stuffing,
  because it biases the sample — the customer's core product. IP is used
  for rate-limiting scripted floods, not as a dedup key; thresholds are
  chosen generously.

### Deliberately rejected (see `01-stack.md` "Что сознательно не берём")

Kafka/NATS (Redis already plays the buffering role), Kubernetes/Helm (local
run only, pre-scaling described as text), gRPC (browser client), ClickHouse
(individual votes are never stored, by design), Scylla/Cassandra (same
reason), WebSockets for live results (polling once/sec is enough for a
handful of admin users), CAPTCHA/Play Integrity (directly conflicts with the
"basic, not adversarial" dedup requirement and hurts conversion), a
hand-rolled Go rate limiter (must be shared across instances, so it's Redis
+ Lua anyway), Fiber/Echo/Gin (fasthttp incompatibility or no benefit over
stdlib+chi), GORM/sqlc (schema too small to justify), zap/zerolog (hot path
doesn't log), memcached (no Lua, no atomic `SET NX` continuation).

## Testing strategy (planned)

- Unit tests: pure logic — poll validation, voting-window checks, token
  sign/parse, counter sharding.
- Integration tests via `testcontainers-go` against real Redis + Postgres
  (deliberately not `miniredis`, since it emulates Lua incompletely and the
  Lua script is the critical piece to verify) — covers the Lua script's
  race behavior, vote idempotency, snapshot correctness.
- A dedicated concurrency test: N goroutines vote with the same token
  concurrently, asserting exactly one accepted vote.
- Load testing with `grafana/k6` using a `ramping-arrival-rate` scenario
  shaped to match the arrival-time model in `02-load-model.md` §3 (chosen
  over vegeta/wrk because the vote flow is stateful: fetch a token, then
  vote). Results are recorded in the README as measurements, not
  extrapolated numbers presented as measurements — a local machine cannot
  actually generate 75k RPS, so only single-instance ceilings are measured
  and cluster numbers are linearly extrapolated with the coefficient stated
  explicitly.

## Open items (per docs/ai/README.md, not yet written)

`03-deduplication.md` (dedup scheme, resolving the conflict with anonymity),
`04-data-model.md` (schema, poll states, Redis key layout),
`05-api-contract.md` (public + admin API), `adr/`, `sessions/` (AI dialogue
logs including dead ends), `what-ai-got-wrong.md`.
