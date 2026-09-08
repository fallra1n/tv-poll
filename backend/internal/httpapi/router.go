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

	"github.com/fallra1n/tvpoll/internal/config"
)

// Deps holds everything the routers need. It grows as handles are added
// (poll store, vote store, caches, ...); kept as one struct so handlers
// take a single dependency, not a long parameter list.
type Deps struct {
	Config config.Config
	Logger *slog.Logger
	DB     *pgxpool.Pool
	Redis  *redis.Client
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

	r.Route("/v1/polls/{pollId}", func(pub chi.Router) {
		// Populated in later steps: GET /, POST /token, POST /votes.
	})

	r.Route("/v1/admin", func(admin chi.Router) {
		admin.Use(adminAuth(deps.Config.AdminToken))
		// Populated in later steps: polls CRUD-ish + transitions + results.
	})

	r.Route("/internal", func(internal chi.Router) {
		internal.Use(adminAuth(deps.Config.AdminToken))
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
