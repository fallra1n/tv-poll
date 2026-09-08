package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/fallra1n/tvpoll/internal/cache"
	"github.com/fallra1n/tvpoll/internal/config"
	"github.com/fallra1n/tvpoll/internal/domain"
	"github.com/fallra1n/tvpoll/internal/ratelimit"
)

// pollPublicResponse mirrors api/openapi.yaml's PollPublic schema.
// Deliberately no "state": it changes over the poll's life
// (scheduled -> open -> closed) while this response is cached for a day
// as immutable — a field that changes would break the premise the
// caching relies on. Clients infer "is voting open" from
// opens_at/closes_at against wall-clock time; POST /votes remains the
// sole authority on whether a vote is actually accepted (it reads the
// live cache, not this response).
type pollPublicResponse struct {
	ID       string               `json:"id"`
	Question string               `json:"question"`
	Options  []pollOptionResponse `json:"options"`
	OpensAt  time.Time            `json:"opens_at"`
	ClosesAt time.Time            `json:"closes_at"`
}

func newPollPublicResponse(d cache.PollDefinition) pollPublicResponse {
	options := make([]pollOptionResponse, len(d.Options))
	for i, o := range d.Options {
		options[i] = pollOptionResponse{ID: o.Ordinal, Label: o.Label}
	}

	var opensAt, closesAt time.Time
	if d.ScheduledAt != nil {
		opensAt = *d.ScheduledAt
	}
	if d.ClosesAt != nil {
		closesAt = *d.ClosesAt
	}

	return pollPublicResponse{
		ID:       d.ID.String(),
		Question: d.Question,
		Options:  options,
		OpensAt:  opensAt,
		ClosesAt: closesAt,
	}
}

// getPollHandler implements GET /v1/polls/{pollId}. A poll is visible
// here from `scheduled` onward, not only `open` — the CDN in front of
// this endpoint needs something to cache before air time, not starting
// exactly when the QR code appears on screen
// (docs/ai/05-api-contract.md, "почему GET открыт уже в scheduled").
func getPollHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "pollId"))
		if err != nil {
			notFoundPoll(w)
			return
		}

		def, err := deps.PollCache.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				notFoundPoll(w)
				return
			}
			deps.Logger.Error("get poll", "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to get poll")
			return
		}
		if def.State == domain.PollDraft {
			notFoundPoll(w)
			return
		}

		w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
		writeJSON(w, http.StatusOK, newPollPublicResponse(def))
	}
}

// notFoundPoll always sets no-store: a cached 404 for a poll that
// publishes a second later would keep shadowing the real content once
// it exists (docs/ai/05-api-contract.md review, C1/A3 area).
func notFoundPoll(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	writeError(w, http.StatusNotFound, "not_found", "poll not found")
}

type voteTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// issueVoteTokenHandler implements POST /v1/polls/{pollId}/token — the
// first step of the two-step voting flow (docs/ai/01-stack.md). Issuing
// a token never touches Redis (docs/ai/05-api-contract.md: registering
// it there up front would make the first real vote look like a
// duplicate) — it only needs the poll's closes_at, read from the same
// cache the vote hot path uses.
//
// limiter is shared with castVoteHandler: docs/ai/03-deduplication.md
// §3.1 requires one bucket per IP across both endpoints, not one each —
// otherwise a script could still mint tokens as fast as it likes and
// only get throttled once it tries to spend them.
func issueVoteTokenHandler(deps Deps, limiter *ratelimit.Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow(clientIP(r)) {
			writeRateLimited(w)
			return
		}

		id, err := uuid.Parse(chi.URLParam(r, "pollId"))
		if err != nil {
			notFoundPoll(w)
			return
		}

		def, err := deps.PollCache.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				notFoundPoll(w)
				return
			}
			deps.Logger.Error("issue vote token: load poll", "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to issue vote token")
			return
		}
		// A draft poll is 404 to the public, same as GET. ClosesAt is
		// guaranteed non-nil for every other state (set together with
		// scheduled_at, which draft is the only state without) — the nil
		// check guards that invariant instead of trusting it blindly,
		// since a nil dereference here would panic, not degrade.
		if def.State == domain.PollDraft || def.ClosesAt == nil {
			notFoundPoll(w)
			return
		}

		token, err := domain.IssueVoteToken(id, *def.ClosesAt, deps.Config.VoteTokenSecret)
		if err != nil {
			deps.Logger.Error("issue vote token: sign", "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to issue vote token")
			return
		}

		setVoteTokenCookie(w, deps.Config, token, *def.ClosesAt)
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusCreated, voteTokenResponse{Token: token, ExpiresAt: *def.ClosesAt})
	}
}

// setVoteTokenCookie implements the "two levels" delivery from
// docs/ai/01-stack.md ("Граница с фронтендом", п.2): the server sets a
// cookie AND returns the token in the body, and the frontend also
// persists it to localStorage — vote_token/votes accepts whichever
// source shows up. HttpOnly is deliberately false so the frontend can
// read the cookie to duplicate it into localStorage.
//
// SameSite=None requires Secure in real browsers; CookieSecure is false
// only for local plain-http development
// (docs/ai/05-api-contract.md) — under that setting a real browser
// won't actually keep this cookie (it isn't a bug here, it's the
// browser enforcing the same rule), which is exactly why the token is
// also returned in the response body: local testing exercises that path
// via curl or the body/localStorage fallback, not the cookie.
func setVoteTokenCookie(w http.ResponseWriter, cfg config.Config, token string, expiresAt time.Time) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "vote_token",
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   cfg.CookieSecure,
		SameSite: http.SameSiteNoneMode,
		HttpOnly: false,
	})
}
