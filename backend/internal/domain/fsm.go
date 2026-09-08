package domain

import (
	"fmt"
	"time"
)

// AllowedTransition reports whether "to" is a legal edge in the poll FSM
// draft -> scheduled -> open -> closed (docs/ai/01-stack.md). scheduled
// can also go straight to closed — cancelling a poll before it airs.
// Every other pair (including reopening a closed poll, or open -> draft)
// is rejected.
func AllowedTransition(from, to PollState) bool {
	switch from {
	case PollDraft:
		return to == PollScheduled
	case PollScheduled:
		return to == PollOpen || to == PollClosed
	case PollOpen:
		return to == PollClosed
	default:
		return false
	}
}

// TransitionError reports an FSM edge that doesn't exist for the poll's
// current state. The HTTP layer maps it to 409, per api/openapi.yaml
// ("Переход, не разрешённый текущим состоянием ... — 409").
type TransitionError struct {
	From PollState
	To   PollState
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("cannot transition from %q to %q", e.From, e.To)
}

// TransitionRequest is the validated input for
// POST /v1/admin/polls/{id}/transitions. ScheduledAt is only meaningful
// (and required) when To == PollScheduled.
type TransitionRequest struct {
	To          PollState
	ScheduledAt *time.Time
}

// Validate checks request shape — not FSM legality against the poll's
// *current* state, which the store checks against AllowedTransition once
// it has read that state (under a row lock, to avoid a race between two
// concurrent transition calls).
func (r TransitionRequest) Validate(now time.Time) error {
	var issues []string

	if !isTransitionTarget(r.To) {
		issues = append(issues, "to must be one of scheduled, open, closed")
	}

	if r.To == PollScheduled {
		if r.ScheduledAt == nil {
			issues = append(issues, "scheduled_at is required when to=scheduled")
		} else if !r.ScheduledAt.After(now) {
			issues = append(issues, "scheduled_at must be in the future")
		}
	}

	if len(issues) == 0 {
		return nil
	}
	return &ValidationError{Issues: issues}
}

// isTransitionTarget excludes PollDraft: draft is only ever the initial
// state a poll is created in (api/openapi.yaml: "draft недостижим как
// цель перехода — это только начальное состояние"), never a transition
// target.
func isTransitionTarget(s PollState) bool {
	switch s {
	case PollScheduled, PollOpen, PollClosed:
		return true
	}
	return false
}
