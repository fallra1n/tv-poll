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
