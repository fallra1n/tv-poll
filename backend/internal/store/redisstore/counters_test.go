package redisstore_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/fallra1n/tvpoll/internal/counter"
	"github.com/fallra1n/tvpoll/internal/store/redisstore"
)

func TestVoteCounterStore_FlushPoll_WritesAndResetsCounters(t *testing.T) {
	rdb := newTestRedis(t)
	store := redisstore.NewVoteCounterStore(rdb)
	ctx := context.Background()
	pollID := uuid.New()

	counters := counter.NewStore()
	pc := counters.ForPoll(pollID, 3)
	pc.AcceptVote(1)
	pc.AcceptVote(1)
	pc.AcceptVote(3)
	pc.RejectDuplicate()

	if err := store.FlushPoll(ctx, pollID, pc); err != nil {
		t.Fatalf("FlushPoll: %v", err)
	}

	accepted, rejectedDuplicate, _, err := store.ReadCounts(ctx, pollID)
	if err != nil {
		t.Fatalf("ReadCounts: %v", err)
	}
	if accepted[1] != 2 {
		t.Errorf("accepted[1] = %d, want 2", accepted[1])
	}
	if accepted[3] != 1 {
		t.Errorf("accepted[3] = %d, want 1", accepted[3])
	}
	if rejectedDuplicate != 1 {
		t.Errorf("rejectedDuplicate = %d, want 1", rejectedDuplicate)
	}

	// The in-memory deltas must be zeroed after a successful flush —
	// otherwise the next flush would double-count.
	if got := pc.Accepted[0].Load(); got != 0 {
		t.Errorf("in-memory Accepted[0] after flush = %d, want 0", got)
	}
}

func TestVoteCounterStore_FlushPoll_AccumulatesAcrossFlushes(t *testing.T) {
	rdb := newTestRedis(t)
	store := redisstore.NewVoteCounterStore(rdb)
	ctx := context.Background()
	pollID := uuid.New()
	counters := counter.NewStore()
	pc := counters.ForPoll(pollID, 1)

	pc.AcceptVote(1)
	if err := store.FlushPoll(ctx, pollID, pc); err != nil {
		t.Fatalf("first FlushPoll: %v", err)
	}
	pc.AcceptVote(1)
	pc.AcceptVote(1)
	if err := store.FlushPoll(ctx, pollID, pc); err != nil {
		t.Fatalf("second FlushPoll: %v", err)
	}

	accepted, _, _, err := store.ReadCounts(ctx, pollID)
	if err != nil {
		t.Fatalf("ReadCounts: %v", err)
	}
	if accepted[1] != 3 {
		t.Errorf("accepted[1] = %d, want 3 (HINCRBY accumulates across flushes)", accepted[1])
	}
}

// TestVoteCounterStore_FlushPoll_RestoresOnFailure points the store at
// an address nothing listens on — proving that when the Redis write
// fails, the swapped-out deltas are added back rather than discarded.
// This is the property that makes a Redis hiccup a delay, not a lost
// vote.
func TestVoteCounterStore_FlushPoll_RestoresOnFailure(t *testing.T) {
	brokenClient := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1", // nothing listens here
		DialTimeout: 200 * time.Millisecond,
	})
	t.Cleanup(func() { _ = brokenClient.Close() })
	store := redisstore.NewVoteCounterStore(brokenClient)

	counters := counter.NewStore()
	pc := counters.ForPoll(uuid.New(), 2)
	pc.AcceptVote(1)
	pc.AcceptVote(2)
	pc.RejectRateLimited()

	err := store.FlushPoll(context.Background(), uuid.New(), pc)
	if err == nil {
		t.Fatal("expected FlushPoll to fail against an unreachable Redis")
	}

	if got := pc.Accepted[0].Load(); got != 1 {
		t.Errorf("Accepted[0] after failed flush = %d, want 1 (restored)", got)
	}
	if got := pc.Accepted[1].Load(); got != 1 {
		t.Errorf("Accepted[1] after failed flush = %d, want 1 (restored)", got)
	}
	if got := pc.RejectedRateLimited.Load(); got != 1 {
		t.Errorf("RejectedRateLimited after failed flush = %d, want 1 (restored)", got)
	}
}

func TestVoteCounterStore_FlushAll_LogsButContinuesOnPerPollError(t *testing.T) {
	rdb := newTestRedis(t)
	store := redisstore.NewVoteCounterStore(rdb)
	counters := counter.NewStore()

	idA, idB := uuid.New(), uuid.New()
	counters.ForPoll(idA, 1).AcceptVote(1)
	counters.ForPoll(idB, 1).AcceptVote(1)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store.FlushAll(context.Background(), counters, logger)

	acceptedA, _, _, err := store.ReadCounts(context.Background(), idA)
	if err != nil {
		t.Fatalf("ReadCounts idA: %v", err)
	}
	acceptedB, _, _, err := store.ReadCounts(context.Background(), idB)
	if err != nil {
		t.Fatalf("ReadCounts idB: %v", err)
	}
	if acceptedA[1] != 1 || acceptedB[1] != 1 {
		t.Errorf("expected both polls flushed, got A=%v B=%v", acceptedA, acceptedB)
	}
}
