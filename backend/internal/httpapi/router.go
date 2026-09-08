// Package httpapi wires the public, admin, and internal HTTP routers.
// Public and admin endpoints are deliberately split into separate
// sub-routers with different middleware stacks (docs/ai/01-stack.md,
// "Аутентификация админки"): public has no auth but is rate-limited,
// admin has auth but no rate limit. /metrics lives on a separate
// listener entirely (see cmd/api/main.go), not in this router.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/fallra1n/tvpoll/internal/cache"
	"github.com/fallra1n/tvpoll/internal/config"
	"github.com/fallra1n/tvpoll/internal/counter"
	"github.com/fallra1n/tvpoll/internal/ratelimit"
	"github.com/fallra1n/tvpoll/internal/store/postgres"
	"github.com/fallra1n/tvpoll/internal/store/redisstore"
)

// Deps holds everything the routers need. It grows as handles are added
// (poll store, vote store, caches, ...); kept as one struct so handlers
// take a single dependency, not a long parameter list.
type Deps struct {
	Config    config.Config
	Logger    *slog.Logger
	DB        *pgxpool.Pool
	Redis     *redis.Client
	Polls     *postgres.PollStore
	PollCache *cache.Cache
	Dedup     *redisstore.DedupStore
	Counters  *counter.Store
}

// New builds the top-level handler for HTTPAddr (everything except
// /metrics).
func New(deps Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(deps.Logger))
	r.Use(corsMiddleware(deps.Config.CORSAllowedOrigins))
	r.Use(middleware.Timeout(10 * time.Second))

	r.Get("/healthz", healthzHandler(deps))

	// Shared by every adminAuth-guarded router below: throttles repeated
	// *failed* bearer-token attempts per IP (review finding C8 —
	// subtle.ConstantTimeCompare defends the comparison itself against a
	// timing side-channel, not the endpoint against brute force). Kept
	// deliberately rough — a handful of failed attempts per second is far
	// above any legitimate typo rate and far below what matters for a
	// single static token with no lockout/rotation UX to protect.
	adminFailLimiter := ratelimit.New(1, 10, 10_000)

	// Shared across /token and /votes — one bucket per IP for both, per
	// docs/ai/03-deduplication.md §3.1 ("Лимит общий для /token и
	// /votes"): rate-limiting only one of the two would let a script mint
	// tokens as fast as it likes and only get throttled at the second
	// endpoint. Threshold and burst come from config (defaults: 6 rps /
	// burst 60, matching the draft "60 req/10s per IP" from that doc);
	// maxKeys bounds memory across however many distinct IPs a broadcast
	// brings, per internal/ratelimit's own doc comment.
	voteRateLimiter := ratelimit.New(deps.Config.RateLimitRPS, deps.Config.RateLimitBurst, 100_000)

	r.Route("/v1/polls/{pollId}", func(pub chi.Router) {
		pub.Get("/", getPollHandler(deps, voteRateLimiter))
		pub.Post("/token", issueVoteTokenHandler(deps, voteRateLimiter))
		pub.Post("/votes", castVoteHandler(deps, voteRateLimiter))
	})

	r.Route("/v1/admin", func(admin chi.Router) {
		admin.Use(adminAuth(deps.Config.AdminToken, adminFailLimiter))
		admin.Post("/polls", createPollHandler(deps))
		admin.Get("/polls", listPollsHandler(deps))
		admin.Get("/polls/{pollId}", getPollAdminHandler(deps))
		admin.Post("/polls/{pollId}/transitions", transitionPollHandler(deps))
		// Populated in later steps: results.
	})

	r.Route("/internal", func(internal chi.Router) {
		internal.Use(adminAuth(deps.Config.AdminToken, adminFailLimiter))
		// Populated in step 9: POST /warmup.
	})

	return r
}

// healthzHandler pings Postgres and Redis with a short timeout so
// docker-compose / an orchestrator can tell "process is up" from
// "process is actually usable" apart. Cheap enough to poll: this is not
// the vote hot path.
func healthzHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		status := map[string]string{"status": "ok"}
		healthy := true

		if err := deps.DB.Ping(ctx); err != nil {
			status["postgres"] = "down"
			healthy = false
		} else {
			status["postgres"] = "ok"
		}

		if err := deps.Redis.Ping(ctx).Err(); err != nil {
			status["redis"] = "down"
			healthy = false
		} else {
			status["redis"] = "ok"
		}

		if !healthy {
			status["status"] = "degraded"
			writeJSON(w, http.StatusServiceUnavailable, status)
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}
