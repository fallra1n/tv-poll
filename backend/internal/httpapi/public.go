package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/fallra1n/tvpoll/internal/cache"
	"github.com/fallra1n/tvpoll/internal/config"
	"github.com/fallra1n/tvpoll/internal/domain"
	"github.com/fallra1n/tvpoll/internal/metrics"
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
//
// limiter: added alongside the vote handler — this endpoint falls
// through to Postgres on every cache miss (fetch-through, see
// internal/cache), and unlike /token and /votes it started out with no
// rate limit at all, which meant a flood of requests for bogus poll IDs
// would hit Postgres on every single one. In production a CDN sits in
// front of this response and absorbs that traffic
// (docs/ai/02-load-model.md §6.1); locally, this is the only thing
// standing in for it.
func getPollHandler(deps Deps, limiter *ratelimit.Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "pollId"))
		if err != nil {
			notFoundPoll(w)
			return
		}

		if !limiter.Allow(voteRateLimitKey(id, r, deps.Config.TrustProxyHeaders)) {
			writeRateLimited(w)
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
// only get throttled once it tries to spend them. The bucket key is
// scoped to (poll_id, ip), not ip alone (docs/ai/04-data-model.md §2.4:
// the original Redis key was `rl:{poll_id}:{ip}` for exactly this
// reason) — otherwise one IP voting on two different polls would share a
// single budget between them.
func issueVoteTokenHandler(deps Deps, limiter *ratelimit.Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "pollId"))
		if err != nil {
			notFoundPoll(w)
			return
		}

		if !limiter.Allow(voteRateLimitKey(id, r, deps.Config.TrustProxyHeaders)) {
			writeRateLimited(w)
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

// dedupGrace pads the dedup key's TTL past closes_at
// (docs/ai/03-deduplication.md §2.3): slack for in-flight requests and
// clock drift between instances, not a security boundary.
const dedupGrace = 60 * time.Second

type voteRequest struct {
	OptionID int `json:"option_id"`
}

type voteAcceptedResponse struct {
	Status   string `json:"status"`
	OptionID int    `json:"option_id"`
}

type voteRejectedResponse struct {
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	OptionID *int   `json:"option_id,omitempty"`
}

func writeVoteRejected(w http.ResponseWriter, status int, reason string, optionID *int) {
	writeJSON(w, status, voteRejectedResponse{Status: "rejected", Reason: reason, OptionID: optionID})
}

// writeVoteRateLimited answers 429 on POST /votes specifically —
// api/openapi.yaml declares this endpoint's 429 body as VoteRejected
// (reason=rate_limited), unlike the Error envelope used by GET
// /v1/polls/{id} and POST /token's 429 (writeRateLimited). Found by
// diffing the spec against this handler, not by any test — the
// generated TS client types this response as VoteRejected, and until
// this fix it never matched at runtime (docs/ai/what-ai-got-wrong.md).
func writeVoteRateLimited(w http.ResponseWriter) {
	setRateLimitHeaders(w)
	writeVoteRejected(w, http.StatusTooManyRequests, "rate_limited", nil)
}

// castVoteHandler implements POST /v1/polls/{pollId}/votes — the one
// endpoint the backend must survive at peak load
// (docs/ai/02-load-model.md §6.1). Check order mirrors this session's
// plan exactly: rate limit (0 I/O) -> token signature (0 I/O) -> cached
// poll definition (0 I/O in steady state) -> window/option validation
// (0 I/O) -> exactly one Redis round-trip (the dedup claim) -> in-memory
// counter increment (0 I/O).
//
// Deliberately reads the poll via GetCached, not the fetch-through Get
// used by GET /v1/polls/{id} and /token: this is the one endpoint the
// entire load model's numbers are about, and "never touches Postgres"
// (docs/ai/02-load-model.md §4.2) should hold for it without a "well,
// rarely" caveat. A cache miss here — a genuinely nonexistent poll, or
// (rare, self-healing within one refresh tick) a vote arriving in the
// sub-second window before the first background refresh after a fresh
// process start — is answered as 404 rather than falling through to
// Postgres.
func castVoteHandler(deps Deps, limiter *ratelimit.Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() { metrics.VoteHandlerDuration.Observe(time.Since(start).Seconds()) }()

		pollID, err := uuid.Parse(chi.URLParam(r, "pollId"))
		if err != nil {
			notFoundPoll(w)
			return
		}

		if !limiter.Allow(voteRateLimitKey(pollID, r, deps.Config.TrustProxyHeaders)) {
			metrics.VotesRejected.WithLabelValues("rate_limited").Inc()
			writeVoteRateLimited(w)
			return
		}

		wireToken, ok := voteTokenFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "vote token missing")
			return
		}

		now := time.Now()
		secrets := []string{deps.Config.VoteTokenSecret, deps.Config.VoteTokenSecretPrev}
		token, err := domain.VerifyVoteToken(wireToken, pollID, now, secrets)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "vote token invalid or expired")
			return
		}

		def, ok := deps.PollCache.GetCached(pollID)
		if !ok {
			notFoundPoll(w)
			return
		}

		// Re-checked here even though only `open` polls should be
		// accepting votes: the schedule worker is eventually consistent,
		// and this remains the same
		// belt-and-suspenders check the original Lua design made
		// (docs/ai/04-data-model.md §2.3: state OR closes_at violated ->
		// closed) — a poll manually left open past its window must still
		// stop accepting votes.
		if def.State != domain.PollOpen || def.ClosesAt == nil || !now.Before(*def.ClosesAt) {
			metrics.VotesRejected.WithLabelValues("closed").Inc()
			writeVoteRejected(w, http.StatusConflict, "closed", nil)
			return
		}

		var req voteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OptionID < 1 || req.OptionID > def.OptionCount() {
			writeError(w, http.StatusBadRequest, "invalid_option", "option_id does not exist in this poll")
			return
		}

		ttl := time.Until(*def.ClosesAt) + dedupGrace
		prevOptionID, accepted, err := deps.Dedup.TryClaim(r.Context(), pollID, token.NonceString(), req.OptionID, ttl)
		if err != nil {
			deps.Logger.Error("cast vote: redis dedup claim failed", "error", err)
			writeError(w, http.StatusServiceUnavailable, "redis_unavailable", "voting is temporarily unavailable")
			return
		}

		counters := deps.Counters.ForPoll(pollID, def.OptionCount())
		if !accepted {
			counters.RejectDuplicate()
			metrics.VotesRejected.WithLabelValues("duplicate").Inc()
			writeVoteRejected(w, http.StatusConflict, "duplicate", &prevOptionID)
			return
		}

		counters.AcceptVote(req.OptionID)
		metrics.VotesAccepted.Inc()
		writeJSON(w, http.StatusCreated, voteAcceptedResponse{Status: "accepted", OptionID: req.OptionID})
	}
}

// voteTokenFromRequest checks the X-Vote-Token header first, then the
// vote_token cookie — either source is accepted
// (docs/ai/01-stack.md, "Граница с фронтендом", п.2).
func voteTokenFromRequest(r *http.Request) (string, bool) {
	if h := r.Header.Get("X-Vote-Token"); h != "" {
		return h, true
	}
	if c, err := r.Cookie("vote_token"); err == nil && c.Value != "" {
		return c.Value, true
	}
	return "", false
}
