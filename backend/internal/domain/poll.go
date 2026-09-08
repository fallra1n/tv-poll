// Package domain holds types and pure logic (validation, the poll FSM,
// vote-token signing) shared across the HTTP and storage layers — no I/O
// here, so it's the layer covered by unit tests without testcontainers.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// PollState is the poll lifecycle FSM: draft -> scheduled -> open -> closed
// (docs/ai/01-stack.md). Stored as TEXT + CHECK in Postgres, not an ENUM
// (docs/ai/04-data-model.md §1.1: adding a value to ENUM needs ALTER TYPE,
// not worth it for a 4-value set unlikely to change).
type PollState string

const (
	PollDraft     PollState = "draft"
	PollScheduled PollState = "scheduled"
	PollOpen      PollState = "open"
	PollClosed    PollState = "closed"
)

// Valid reports whether s is one of the four known FSM states — used to
// reject a malformed ?state= filter before it ever reaches SQL.
func (s PollState) Valid() bool {
	switch s {
	case PollDraft, PollScheduled, PollOpen, PollClosed:
		return true
	}
	return false
}

// PollOption is one answer choice. Ordinal (1..N) is the wire option_id —
// a small per-poll integer, not a UUID, because it's a hot-path field on
// every vote (docs/ai/04-data-model.md §1.2).
type PollOption struct {
	Ordinal int
	Label   string
}

// Poll mirrors the polls table. ScheduledAt is the single input to
// ClosesAt (computed by a DB trigger); OpenedAt/ClosedAt record when the
// poll actually transitioned, which can differ from the schedule (manual
// override, prescaling jitter, early close) — see
// docs/ai/04-data-model.md §1.1.
type Poll struct {
	ID                  uuid.UUID
	Question            string
	State               PollState
	ScheduledAt         *time.Time
	VotingWindowSeconds int
	ClosesAt            *time.Time
	OpenedAt            *time.Time
	ClosedAt            *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Options             []PollOption
}
