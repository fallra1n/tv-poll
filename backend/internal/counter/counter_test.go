package counter

import (
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestPollCounters_AcceptVote(t *testing.T) {
	s := NewStore()
	pollID := uuid.New()
	pc := s.ForPoll(pollID, 3)

	pc.AcceptVote(1)
	pc.AcceptVote(1)
	pc.AcceptVote(3)

	if got := pc.Accepted[0].Load(); got != 2 {
		t.Errorf("option 1 = %d, want 2", got)
	}
	if got := pc.Accepted[1].Load(); got != 0 {
		t.Errorf("option 2 = %d, want 0", got)
	}
	if got := pc.Accepted[2].Load(); got != 1 {
		t.Errorf("option 3 = %d, want 1", got)
	}
}

func TestPollCounters_Rejections(t *testing.T) {
	s := NewStore()
	pc := s.ForPoll(uuid.New(), 2)

	pc.RejectDuplicate()
	pc.RejectDuplicate()
	pc.RejectRateLimited()

	if got := pc.RejectedDuplicate.Load(); got != 2 {
		t.Errorf("RejectedDuplicate = %d, want 2", got)
	}
	if got := pc.RejectedRateLimited.Load(); got != 1 {
		t.Errorf("RejectedRateLimited = %d, want 1", got)
	}
}

func TestStore_ForPoll_SamePollReturnsSameCounters(t *testing.T) {
	s := NewStore()
	pollID := uuid.New()

	a := s.ForPoll(pollID, 5)
	b := s.ForPoll(pollID, 5)
	if a != b {
		t.Fatal("expected ForPoll to return the same *PollCounters for the same poll ID")
	}

	a.AcceptVote(2)
	if got := b.Accepted[1].Load(); got != 1 {
		t.Errorf("expected increment through a to be visible through b, got %d", got)
	}
}

func TestStore_ForPoll_DifferentPollsAreIndependent(t *testing.T) {
	s := NewStore()
	a := s.ForPoll(uuid.New(), 2)
	b := s.ForPoll(uuid.New(), 2)

	a.AcceptVote(1)
	if got := b.Accepted[0].Load(); got != 0 {
		t.Errorf("expected poll b's counters to be unaffected by poll a's vote, got %d", got)
	}
}

func TestPollCounters_SwapDeltas(t *testing.T) {
	s := NewStore()
	pc := s.ForPoll(uuid.New(), 3)

	pc.AcceptVote(1)
	pc.AcceptVote(2)
	pc.AcceptVote(2)
	pc.RejectDuplicate()
	pc.RejectDuplicate()
	pc.RejectRateLimited()

	accepted, rejectedDuplicate, rejectedRateLimited := pc.SwapDeltas()
	if want := []int64{1, 2, 0}; accepted[0] != want[0] || accepted[1] != want[1] || accepted[2] != want[2] {
		t.Errorf("accepted = %v, want %v", accepted, want)
	}
	if rejectedDuplicate != 2 {
		t.Errorf("rejectedDuplicate = %d, want 2", rejectedDuplicate)
	}
	if rejectedRateLimited != 1 {
		t.Errorf("rejectedRateLimited = %d, want 1", rejectedRateLimited)
	}

	// Everything must be reset to zero after the swap.
	accepted2, dup2, rl2 := pc.SwapDeltas()
	for i, v := range accepted2 {
		if v != 0 {
			t.Errorf("accepted[%d] after swap = %d, want 0", i, v)
		}
	}
	if dup2 != 0 || rl2 != 0 {
		t.Errorf("rejected counters after swap = (%d, %d), want (0, 0)", dup2, rl2)
	}
}

func TestPollCounters_SwapDeltas_NewVotesAfterSwapAreNotLost(t *testing.T) {
	s := NewStore()
	pc := s.ForPoll(uuid.New(), 2)

	pc.AcceptVote(1)
	pc.SwapDeltas() // simulates a flush draining the first vote

	pc.AcceptVote(1) // a second vote lands after the flush
	accepted, _, _ := pc.SwapDeltas()
	if accepted[0] != 1 {
		t.Errorf("accepted[0] = %d, want 1 (the vote after the first swap)", accepted[0])
	}
}

func TestPollCounters_RestoreDeltas(t *testing.T) {
	s := NewStore()
	pc := s.ForPoll(uuid.New(), 2)

	pc.AcceptVote(1)
	pc.AcceptVote(2)
	pc.RejectDuplicate()
	accepted, dup, rl := pc.SwapDeltas() // simulates draining before a failed flush

	// A new vote arrives while the (failed) flush was in flight.
	pc.AcceptVote(1)

	pc.RestoreDeltas(accepted, dup, rl)

	if got := pc.Accepted[0].Load(); got != 2 { // 1 restored + 1 new
		t.Errorf("Accepted[0] = %d, want 2 (restored + concurrent new vote)", got)
	}
	if got := pc.Accepted[1].Load(); got != 1 {
		t.Errorf("Accepted[1] = %d, want 1", got)
	}
	if got := pc.RejectedDuplicate.Load(); got != 1 {
		t.Errorf("RejectedDuplicate = %d, want 1", got)
	}
}

func TestStore_Range(t *testing.T) {
	s := NewStore()
	idA, idB := uuid.New(), uuid.New()
	s.ForPoll(idA, 2).AcceptVote(1)
	s.ForPoll(idB, 3).AcceptVote(2)

	seen := map[uuid.UUID]bool{}
	s.Range(func(pollID uuid.UUID, pc *PollCounters) {
		seen[pollID] = true
	})

	if !seen[idA] || !seen[idB] {
		t.Fatalf("expected Range to visit both polls, saw %v", seen)
	}
	if len(seen) != 2 {
		t.Fatalf("expected exactly 2 polls visited, got %d", len(seen))
	}
}

// TestStore_ForPoll_ConcurrentFirstAccess guards LoadOrStore's role:
// many goroutines racing to create the same poll's counters for the
// first time must all end up sharing one *PollCounters, not each
// creating (and then losing votes to) their own.
func TestStore_ForPoll_ConcurrentFirstAccess(t *testing.T) {
	s := NewStore()
	pollID := uuid.New()

	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			s.ForPoll(pollID, 1).AcceptVote(1)
		}()
	}
	wg.Wait()

	pc := s.ForPoll(pollID, 1)
	if got := pc.Accepted[0].Load(); got != n {
		t.Errorf("Accepted[0] = %d, want %d (a vote was lost to a duplicate counters instance)", got, n)
	}
}
