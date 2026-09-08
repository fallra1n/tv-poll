package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
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
