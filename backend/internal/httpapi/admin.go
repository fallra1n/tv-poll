package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/fallra1n/tvpoll/internal/domain"
	"github.com/fallra1n/tvpoll/internal/store/postgres"
)

type pollOptionInput struct {
	Label string `json:"label"`
}

type createPollRequest struct {
	Question            string            `json:"question"`
	Options             []pollOptionInput `json:"options"`
	ScheduledAt         *time.Time        `json:"scheduled_at"`
	VotingWindowSeconds *int              `json:"voting_window_seconds"`
}

func (req createPollRequest) toDomain(defaultWindowSeconds int) domain.CreatePollInput {
	window := defaultWindowSeconds
	if req.VotingWindowSeconds != nil {
		window = *req.VotingWindowSeconds
	}

	labels := make([]string, len(req.Options))
	for i, o := range req.Options {
		labels[i] = o.Label
	}

	return domain.CreatePollInput{
		Question:            req.Question,
		Options:             labels,
		ScheduledAt:         req.ScheduledAt,
		VotingWindowSeconds: window,
	}
}

type pollOptionResponse struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

// pollAdminResponse mirrors api/openapi.yaml's PollAdmin schema.
//
// OpensAt is populated from ScheduledAt: the current schema (per this
// session's plan) has no separate opens_at column — scheduled_at is the
// single input to closes_at, and doubles as "when voting is/will be
// open" for the public view. api/openapi.yaml's PollPublic/PollAdmin
// split gets revisited in the GET /v1/polls/{id} step (cacheability),
// this response only needs to be internally consistent until then.
type pollAdminResponse struct {
	ID                  string               `json:"id"`
	Question            string               `json:"question"`
	Options             []pollOptionResponse `json:"options"`
	State               domain.PollState     `json:"state"`
	OpensAt             *time.Time           `json:"opens_at"`
	ClosesAt            *time.Time           `json:"closes_at"`
	ScheduledAt         *time.Time           `json:"scheduled_at"`
	VotingWindowSeconds int                  `json:"voting_window_seconds"`
	CreatedAt           time.Time            `json:"created_at"`
	UpdatedAt           time.Time            `json:"updated_at"`
}

func newPollAdminResponse(p domain.Poll) pollAdminResponse {
	options := make([]pollOptionResponse, len(p.Options))
	for i, o := range p.Options {
		options[i] = pollOptionResponse{ID: o.Ordinal, Label: o.Label}
	}
	return pollAdminResponse{
		ID:                  p.ID.String(),
		Question:            p.Question,
		Options:             options,
		State:               p.State,
		OpensAt:             p.ScheduledAt,
		ClosesAt:            p.ClosesAt,
		ScheduledAt:         p.ScheduledAt,
		VotingWindowSeconds: p.VotingWindowSeconds,
		CreatedAt:           p.CreatedAt,
		UpdatedAt:           p.UpdatedAt,
	}
}

// createPollHandler implements POST /v1/admin/polls.
func createPollHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createPollRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", "malformed request body")
			return
		}

		input := req.toDomain(deps.Config.DefaultVotingWindowSeconds)
		if err := input.Validate(time.Now()); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_poll", err.Error())
			return
		}

		poll, err := deps.Polls.CreatePoll(r.Context(), input)
		if err != nil {
			deps.Logger.Error("create poll", "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to create poll")
			return
		}
		// A poll created directly as scheduled (scheduled_at was given)
		// must be visible to the vote/token cache immediately, not after
		// the next 1s refresh tick — see cache.Cache.Put's doc comment.
		deps.PollCache.Put(poll)

		writeJSON(w, http.StatusCreated, newPollAdminResponse(poll))
	}
}

type listPollsResponse struct {
	Items []pollAdminResponse `json:"items"`
	Total int                 `json:"total"`
}

// listPollsHandler implements GET /v1/admin/polls.
func listPollsHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		filter := postgres.ListPollsFilter{Limit: 20, Offset: 0}

		if raw := q.Get("state"); raw != "" {
			state := domain.PollState(raw)
			if !state.Valid() {
				writeError(w, http.StatusBadRequest, "invalid_query", "state must be one of draft, scheduled, open, closed")
				return
			}
			filter.State = &state
		}

		if raw := q.Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 100 {
				writeError(w, http.StatusBadRequest, "invalid_query", "limit must be an integer between 1 and 100")
				return
			}
			filter.Limit = n
		}

		if raw := q.Get("offset"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 {
				writeError(w, http.StatusBadRequest, "invalid_query", "offset must be a non-negative integer")
				return
			}
			filter.Offset = n
		}

		polls, total, err := deps.Polls.ListPolls(r.Context(), filter)
		if err != nil {
			deps.Logger.Error("list polls", "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to list polls")
			return
		}

		items := make([]pollAdminResponse, len(polls))
		for i, p := range polls {
			items[i] = newPollAdminResponse(p)
		}
		writeJSON(w, http.StatusOK, listPollsResponse{Items: items, Total: total})
	}
}

// getPollAdminHandler implements GET /v1/admin/polls/{pollId}. A
// malformed pollId is treated the same as "no such poll" (404) — from the
// caller's perspective the distinction doesn't matter, and the spec
// doesn't document a 400 for this endpoint.
func getPollAdminHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "pollId"))
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "poll not found")
			return
		}

		poll, err := deps.Polls.GetByID(r.Context(), id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "not_found", "poll not found")
				return
			}
			deps.Logger.Error("get poll", "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to get poll")
			return
		}

		writeJSON(w, http.StatusOK, newPollAdminResponse(poll))
	}
}

type transitionPollRequest struct {
	To          domain.PollState `json:"to"`
	ScheduledAt *time.Time       `json:"scheduled_at"`
}

// transitionPollHandler implements POST /v1/admin/polls/{pollId}/transitions.
func transitionPollHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "pollId"))
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "poll not found")
			return
		}

		var req transitionPollRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", "malformed request body")
			return
		}

		transition := domain.TransitionRequest{To: req.To, ScheduledAt: req.ScheduledAt}
		if err := transition.Validate(time.Now()); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_transition_request", err.Error())
			return
		}

		poll, err := deps.Polls.TransitionPoll(r.Context(), id, transition)
		if err != nil {
			var transErr *domain.TransitionError
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				writeError(w, http.StatusNotFound, "not_found", "poll not found")
			case errors.As(err, &transErr):
				writeError(w, http.StatusConflict, "invalid_transition", transErr.Error())
			default:
				deps.Logger.Error("transition poll", "error", err)
				writeError(w, http.StatusInternalServerError, "internal", "failed to transition poll")
			}
			return
		}
		// Must be visible to the vote/token cache before this response
		// even reaches the admin's browser, not after the next refresh
		// tick — see cache.Cache.Put's doc comment (found via an
		// end-to-end test: a vote fired immediately after scheduled ->
		// open was wrongly rejected as "closed" without this).
		deps.PollCache.Put(poll)

		writeJSON(w, http.StatusOK, newPollAdminResponse(poll))
	}
}

type pollResultOption struct {
	OptionID int     `json:"option_id"`
	Label    string  `json:"label"`
	Count    int64   `json:"count"`
	Share    float64 `json:"share"`
}

type pollResultsRejected struct {
	Duplicate   int64 `json:"duplicate"`
	RateLimited int64 `json:"rate_limited"`
}

type pollResultsSampling struct {
	Enabled bool `json:"enabled"`
	Rate    *int `json:"rate"`
}

type pollResultsResponse struct {
	PollID        string              `json:"poll_id"`
	State         domain.PollState    `json:"state"`
	TotalAccepted int64               `json:"total_accepted"`
	Rejected      pollResultsRejected `json:"rejected"`
	Options       []pollResultOption  `json:"options"`
	SnapshotAt    *time.Time          `json:"snapshot_at"`
	Sampling      pollResultsSampling `json:"sampling"`
}

// getPollResultsHandler implements GET /v1/admin/polls/{pollId}/results.
// Values come from the latest snapshot, not a live Redis read — results
// are eventually consistent by design (docs/ai/02-load-model.md §6.6),
// and a poll that opened less than a snapshot interval ago simply has
// no snapshot yet, rendered as zeros with snapshot_at: null rather than
// an error.
func getPollResultsHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "pollId"))
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "poll not found")
			return
		}

		poll, err := deps.Polls.GetByID(r.Context(), id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "not_found", "poll not found")
				return
			}
			deps.Logger.Error("get poll results: load poll", "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to load results")
			return
		}

		snapshot, hasSnapshot, err := deps.Snapshots.LatestSnapshot(r.Context(), id)
		if err != nil {
			deps.Logger.Error("get poll results: load snapshot", "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to load results")
			return
		}

		resp := pollResultsResponse{
			PollID:  poll.ID.String(),
			State:   poll.State,
			Options: make([]pollResultOption, len(poll.Options)),
		}
		if hasSnapshot {
			resp.TotalAccepted = snapshot.TotalAccepted
			resp.Rejected = pollResultsRejected{
				Duplicate:   snapshot.RejectedDuplicate,
				RateLimited: snapshot.RejectedRateLimited,
			}
			at := snapshot.SnapshotAt
			resp.SnapshotAt = &at
			resp.Sampling = pollResultsSampling{Enabled: snapshot.SamplingEnabled, Rate: snapshot.SamplingRate}
		}

		for i, opt := range poll.Options {
			var count int64
			if hasSnapshot {
				count = snapshot.OptionCounts[strconv.Itoa(opt.Ordinal)]
			}
			var share float64
			if resp.TotalAccepted > 0 {
				share = float64(count) / float64(resp.TotalAccepted)
			}
			resp.Options[i] = pollResultOption{OptionID: opt.Ordinal, Label: opt.Label, Count: count, Share: share}
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

type resultsSnapshotPointOption struct {
	OptionID int   `json:"option_id"`
	Count    int64 `json:"count"`
}

type resultsSnapshotPoint struct {
	At            time.Time                    `json:"at"`
	TotalAccepted int64                        `json:"total_accepted"`
	Options       []resultsSnapshotPointOption `json:"options"`
}

type resultsTimeseriesResponse struct {
	Points []resultsSnapshotPoint `json:"points"`
}

// getPollResultsTimeseriesHandler implements
// GET /v1/admin/polls/{pollId}/results/timeseries — the full snapshot
// history, oldest first, built straight from poll_result_snapshots (a
// side effect of the snapshotting scheme, not a separate mechanism —
// docs/ai/01-stack.md, PostgreSQL section).
func getPollResultsTimeseriesHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "pollId"))
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "poll not found")
			return
		}

		// Confirmed to exist so a typo'd id gets a real 404 instead of
		// an always-empty {points: []}.
		if _, err := deps.Polls.GetByID(r.Context(), id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "not_found", "poll not found")
				return
			}
			deps.Logger.Error("get poll timeseries: load poll", "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to load results")
			return
		}

		snapshots, err := deps.Snapshots.ListSnapshots(r.Context(), id)
		if err != nil {
			deps.Logger.Error("get poll timeseries: load snapshots", "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to load results")
			return
		}

		points := make([]resultsSnapshotPoint, len(snapshots))
		for i, snap := range snapshots {
			options := make([]resultsSnapshotPointOption, 0, len(snap.OptionCounts))
			for key, count := range snap.OptionCounts {
				optionID, convErr := strconv.Atoi(key)
				if convErr != nil {
					continue // this column only ever holds what InsertSnapshot wrote
				}
				options = append(options, resultsSnapshotPointOption{OptionID: optionID, Count: count})
			}
			// Map iteration order is random; sort for a deterministic
			// response instead of a different option order every call.
			sort.Slice(options, func(a, b int) bool { return options[a].OptionID < options[b].OptionID })
			points[i] = resultsSnapshotPoint{At: snap.SnapshotAt, TotalAccepted: snap.TotalAccepted, Options: options}
		}

		writeJSON(w, http.StatusOK, resultsTimeseriesResponse{Points: points})
	}
}
