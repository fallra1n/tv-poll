// Package counter holds in-memory, per-poll vote tallies so the vote
// hot path never writes to Redis or Postgres for the count itself
// (docs/ai/02-load-model.md §4.2 — the only Redis round-trip on the
// vote path is the dedup SET NX, not the counter). A background flusher
// (wired in cmd/api) periodically drains these into Redis.
package counter

import (
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
)

// PollCounters holds one poll's hot-path tallies. Accepted is indexed by
// option_id - 1 (option_id is a 1..N ordinal, docs/ai/04-data-model.md
// §1.2) and fixed-size at creation — a poll's option count never changes
// after creation (no PATCH on published polls), so there's no resize
// path to get wrong.
type PollCounters struct {
	Accepted            []atomic.Int64
	RejectedDuplicate   atomic.Int64
	RejectedRateLimited atomic.Int64
}

// AcceptVote records one accepted vote for optionID (1-indexed).
// Callers must have already validated optionID against the same option
// count this PollCounters was created with — see Store.ForPoll.
func (pc *PollCounters) AcceptVote(optionID int) {
	pc.Accepted[optionID-1].Add(1)
}

func (pc *PollCounters) RejectDuplicate() {
	pc.RejectedDuplicate.Add(1)
}

func (pc *PollCounters) RejectRateLimited() {
	pc.RejectedRateLimited.Add(1)
}

// Store holds one *PollCounters per poll, created lazily on first use.
// A sync.Map fits the access pattern exactly: a handful of distinct keys
// (only ever one poll is actually live at a time,
// docs/ai/02-load-model.md), each read far more often than written —
// every vote after the first for a poll is a pure read, no lock
// contention on the hot path.
type Store struct {
	polls sync.Map // uuid.UUID -> *PollCounters
}

func NewStore() *Store {
	return &Store{}
}

// ForPoll returns the counters for pollID, allocating optionCount
// buckets the first time this poll is seen. Concurrent first calls for
// the same poll race harmlessly — LoadOrStore picks one winner, the
// other allocation is simply discarded.
func (s *Store) ForPoll(pollID uuid.UUID, optionCount int) *PollCounters {
	if v, ok := s.polls.Load(pollID); ok {
		return v.(*PollCounters)
	}
	pc := &PollCounters{Accepted: make([]atomic.Int64, optionCount)}
	actual, _ := s.polls.LoadOrStore(pollID, pc)
	return actual.(*PollCounters)
}
