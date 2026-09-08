package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

type warmupRequest struct {
	PollID *uuid.UUID `json:"poll_id"`
}

// warmupHandler implements POST /internal/warmup — called by the
// broadcast orchestrator ahead of air time, not a human
// (docs/ai/02-load-model.md §6.5: pre-scaling follows the broadcast
// schedule rather than reacting to load, so connection pools need to be
// warm *before* the peak starts, not partway into it — a reactive
// autoscaler's 30-60s reaction time is longer than the ~45s peak
// itself).
//
// The warmup work below completes in low tens of milliseconds, so it
// runs synchronously and the response returns 202 once it's actually
// done: "asynchronous" (api/openapi.yaml) describes that the caller has
// nothing to poll for completion, not that the server defers the work
// to an untracked background goroutine whose failure would go
// unreported.
func warmupHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req warmupRequest
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_json", "malformed request body")
				return
			}
		}

		warmPostgres(r.Context(), deps)
		warmRedis(r.Context(), deps)

		detail := map[string]any{}
		// audit log's poll_id column is a real FK (admin_audit_log ->
		// polls) — only set when the poll actually exists. A nonexistent
		// poll_id must still show up in the audit trail (it's exactly
		// the "operator typo'd the ID" case where a record matters most),
		// so it goes in `detail` regardless; setting it as the FK
		// reference too would violate the constraint and silently drop
		// the whole row — found by testing this exact case end-to-end
		// against the real schema, not assumed.
		var auditPollID *uuid.UUID
		if req.PollID != nil {
			detail["poll_id"] = req.PollID.String()
			// Get, not GetCached: a deliberate fetch-through here, forcing
			// exactly the poll named in the request into the hot-path
			// cache ahead of time — the whole point of naming a poll_id
			// in this call, per api/openapi.yaml's description ("...
			// дополнительно проверяет, что опрос существует и его данные
			// закешированы в памяти инстанса").
			if _, err := deps.PollCache.Get(r.Context(), *req.PollID); err != nil {
				deps.Logger.Warn("warmup: poll_id not found or failed to load", "poll_id", *req.PollID, "error", err)
				detail["poll_found"] = false
			} else {
				auditPollID = req.PollID
				detail["poll_found"] = true
			}
		}

		if err := deps.Polls.InsertAuditLog(r.Context(), "warmup.trigger", auditPollID, detail); err != nil {
			deps.Logger.Error("warmup: audit log", "error", err)
		}

		w.WriteHeader(http.StatusAccepted)
	}
}

// warmPostgres fires one concurrent ping per pool slot, forcing the
// pool to actually open that many connections ahead of the peak instead
// of opening them lazily on the first real request during it — a
// connection handshake is exactly the kind of cost
// docs/ai/02-load-model.md §4.1 says must happen before, not during, the
// peak second.
func warmPostgres(ctx context.Context, deps Deps) {
	n := int(deps.DB.Config().MaxConns)
	if n <= 0 {
		n = 1
	}
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			if err := deps.DB.Ping(pingCtx); err != nil {
				deps.Logger.Warn("warmup: postgres ping failed", "error", err)
			}
		}()
	}
	wg.Wait()
}

func warmRedis(ctx context.Context, deps Deps) {
	n := deps.Redis.Options().PoolSize
	if n <= 0 {
		n = 1
	}
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			if err := deps.Redis.Ping(pingCtx).Err(); err != nil {
				deps.Logger.Warn("warmup: redis ping failed", "error", err)
			}
		}()
	}
	wg.Wait()
}
