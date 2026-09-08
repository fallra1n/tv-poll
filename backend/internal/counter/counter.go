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

// SwapDeltas atomically reads and resets every counter, returning
// whatever had accumulated since the last call (or since creation).
// Used by the background flusher — the returned values are what it
// should HINCRBY into Redis.
//
// Not a single atomic transaction across all fields: a vote landing on
// option i in the instant between swapping field i and field i+1 is
// simply picked up by the next flush instead of this one. It's still
// counted exactly once, just possibly one flush interval later — the
// same eventual-consistency bound already accepted everywhere else in
// this design (docs/ai/02-load-model.md §6.6).
func (pc *PollCounters) SwapDeltas() (accepted []int64, rejectedDuplicate, rejectedRateLimited int64) {
	accepted = make([]int64, len(pc.Accepted))
	for i := range pc.Accepted {
		accepted[i] = pc.Accepted[i].Swap(0)
	}
	return accepted, pc.RejectedDuplicate.Swap(0), pc.RejectedRateLimited.Swap(0)
}

// RestoreDeltas adds previously-swapped-out deltas back. Used when a
// flush attempt fails after already swapping the deltas out — a Redis
// hiccup must delay counts reaching Redis, not lose the votes that were
// about to be flushed.
func (pc *PollCounters) RestoreDeltas(accepted []int64, rejectedDuplicate, rejectedRateLimited int64) {
	for i, d := range accepted {
		if d != 0 {
			pc.Accepted[i].Add(d)
		}
	}
	if rejectedDuplicate != 0 {
		pc.RejectedDuplicate.Add(rejectedDuplicate)
	}
	if rejectedRateLimited != 0 {
		pc.RejectedRateLimited.Add(rejectedRateLimited)
	}
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

// Range calls fn for every poll currently tracked. fn should be quick —
// it runs while walking the underlying sync.Map, concurrently with
// votes still landing on whichever poll fn is currently visiting.
func (s *Store) Range(fn func(pollID uuid.UUID, pc *PollCounters)) {
	s.polls.Range(func(key, value any) bool {
		fn(key.(uuid.UUID), value.(*PollCounters))
		return true
	})
}
