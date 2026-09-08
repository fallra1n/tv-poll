# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

Both `backend/` (Go, `make up` via docker-compose) and `frontend/`
(Bun/React, see `frontend/README.md`) are implemented, verified against each
other with a live `make up` + browser pass, and documented under `docs/ai/`
(including `sessions/` and `what-ai-got-wrong.md`). `frontend/` is not yet
committed to git as of this writing — check `git status` before assuming
either side reflects what's on disk. Tooling exists for both (`go test`,
`bun run lint`/`typecheck`/`test`/`test:e2e`) — run it rather than assuming
state from docs, which have already been caught drifting from the working
tree at least once (see `docs/ai/what-ai-got-wrong.md`).

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

## API contract

The wire contract is fixed in `api/openapi.yaml` (OpenAPI 3.1, hand-written,
validated with `npx @redocly/cli lint api/openapi.yaml`). It lives at the
repo root (not under `backend/`) because `01-stack.md` frames it as the
contract *with the frontend* — a boundary between two separate codebases.
Rationale for the endpoint list and every non-obvious call is in
`docs/ai/05-api-contract.md`; the two load-bearing decisions made there,
not elsewhere:

- **Vote token format**: stateless HMAC-signed opaque token
  (`base64url(poll_id|issued_at|nonce).base64url(HMAC-SHA256(...))`),
  verified locally at vote time with no Redis read at issuance. This closes
  the "HMAC vs opaque-in-Redis" open question `01-stack.md` left open — an
  opaque token pre-registered in Redis at issuance would collide with the
  `SET NX` dedup check and break the first vote.
- **`GET /v1/polls/{id}` is public once a poll is `scheduled`, not only once
  `open`** — otherwise the CDN cache is cold exactly at broadcast start,
  recreating the Coinbase-style collapse the CDN split was meant to prevent.

Endpoints: public (`GET /v1/polls/{id}`, `POST /v1/polls/{id}/token`,
`POST /v1/polls/{id}/votes`), admin (`POST|GET /v1/admin/polls`,
`GET /v1/admin/polls/{id}`, `POST /v1/admin/polls/{id}/transitions` — one
generic FSM-transition endpoint rather than separate schedule/open/close
routes, `GET /v1/admin/polls/{id}/results[/timeseries]`), and internal
(`POST /internal/warmup`, `GET /healthz`, `GET /metrics`). No poll
PATCH/DELETE and no public results endpoint — deliberate, see the "what's
deliberately out" table in `05-api-contract.md`.

## Deduplication

Fixed in `docs/ai/03-deduplication.md`. The core point, worth internalizing
before touching vote-handling code: **the vote token and the IP rate limiter
solve two different problems and neither substitutes for the other.**

- **Token = identity** (dedup). Redis key `d:{poll_id}:{nonce}` (nonce = the
  random component of the HMAC token, not the whole signed value), one
  `SET NX EX` per token, enforced via the Lua script from `01-stack.md`. TTL
  is **not** a flat duration from issuance — both `token.expires_at` and the
  dedup-key TTL equal `poll.closes_at` (+60s grace), because a fixed offset
  from issuance either outlives the voting window or expires before it
  depending on when in the window the token was issued.
- **IP rate limit = anti-flood**, not dedup. A rolling-window limit keyed by
  IP cannot enforce "one vote per person" — CGNAT/shared-Wi-Fi puts many
  real distinct voters behind one IP (blocking them is the R14 false-positive
  cost), and a rolling window resets every window regardless, so it bounds
  *rate* over the poll's 5-minute window, not a *count* for its lifetime.
  It's applied to both `/token` and `/votes` (starving the flood at the
  cheaper endpoint first) with a deliberately generous, unvalidated draft
  threshold (60 req/10s per IP) — tune only after a k6 measurement, per the
  same "generous by design" principle as the degradation ladder in
  `02-load-model.md` §6.7.
- Clearing cookies+localStorage or using incognito bypasses the token dedup
  — accepted deliberately, this is exactly the R2 bar ("basic, not
  adversarial"), not a bug to close.

## Data model

Fixed in `docs/ai/04-data-model.md`: 4 Postgres tables (`polls`,
`poll_options`, `poll_result_snapshots`, `admin_audit_log`) and the full
Redis key inventory (`poll:{id}:meta`, `poll:{id}:counts:{shard}`,
`poll:{id}:rejected:{shard}`, `d:{poll_id}:{nonce}`, `rl:{poll_id}:{ip}`).
The DDL and the updated vote-accept Lua script were both run against real
Postgres 16 and Redis 7 in Docker while writing the doc, not just read over
— worth knowing before trusting either without re-checking against whatever
the schema has drifted to since:

- **Gap this doc closed, not covered by `01`/`02`/`03`**: the vote handler
  needs `poll.state`, `closes_at`, and the valid `option_id` range on every
  vote, but `02-load-model.md` says the hot path never touches Postgres and
  is exactly one Redis round-trip. Fix: `poll:{id}:meta` (a Redis HASH)
  mirrors `state`/`closes_at`/`option_count`, and the vote-accept Lua script
  reads it as part of the *same* `EVALSHA` — still one network round-trip,
  just more Redis-internal commands inside that one call.
- **`poll_options.id` (the wire `option_id`) is a small per-poll ordinal
  (1..N), not a UUID** — it's a hot-path Redis hash field on every vote, so
  keeping it short matters; global uniqueness doesn't, since options are
  never referenced outside their poll.
- **`polls.closes_at` is set by a trigger, not `GENERATED ALWAYS AS`.** The
  generated-column version was tried first and actually fails on real
  Postgres 16 — `timestamptz + interval` is `STABLE`, not `IMMUTABLE`, so
  Postgres rejects it as a generation expression. A `BEFORE INSERT OR
  UPDATE` trigger gives the same "app can't desync `closes_at` from
  `opens_at`/`voting_window_seconds`" guarantee without that restriction.
- **State-transition write order to Postgres vs. the Redis `poll:meta`
  mirror is asymmetric, not "always Postgres-first" or always the reverse**:
  opening a poll writes Postgres first then Redis (a lost Redis write just
  delays voting start — safe); closing a poll writes Redis first then
  Postgres (a lost Postgres write would otherwise leave Redis still
  accepting votes past the real close — unsafe). `/internal/warmup`
  doubles as a reconciliation point rather than adding a separate job.

## Open items (per docs/ai/README.md, not yet written)

`adr/`, `sessions/` (AI dialogue logs including dead ends),
`what-ai-got-wrong.md`. The sampling-mode (degradation level 2) activation
mechanism is also explicitly unresolved — schema has room for it
(`poll_result_snapshots.sampling_enabled/rate`) but no endpoint toggles it;
see `04-data-model.md` §6.
