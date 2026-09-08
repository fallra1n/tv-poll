package redisstore

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/fallra1n/tvpoll/internal/counter"
)

// VoteCounterStore is the Redis side of the accepted/rejected vote
// tallies. FlushPoll drains internal/counter's in-memory deltas into
// Redis via HINCRBY, one key per poll — deliberately *not* sharded like
// the original design's poll:{id}:counts:{0..N-1}: the hot-key problem
// that sharding solved doesn't exist here, because the write rate into
// Redis is one HINCRBY per option per poll per flush interval, not one
// per vote (docs/ai/01-stack.md §4.3's math was about per-vote writes).
// ReadCounts is the read side, used by the snapshotter — a different
// consumer on a different cadence, sharing nothing with FlushPoll but
// the key names.
type VoteCounterStore struct {
	rdb *redis.Client
}

func NewVoteCounterStore(rdb *redis.Client) *VoteCounterStore {
	return &VoteCounterStore{rdb: rdb}
}

func countsKey(pollID uuid.UUID) string {
	return fmt.Sprintf("poll:%s:counts", pollID)
}

func rejectedKey(pollID uuid.UUID) string {
	return fmt.Sprintf("poll:%s:rejected", pollID)
}

// FlushPoll drains pc's accumulated deltas into Redis via one pipeline.
// On any failure the deltas are added back to pc rather than discarded
// — a Redis hiccup delays counts reaching Redis, it doesn't lose votes.
func (s *VoteCounterStore) FlushPoll(ctx context.Context, pollID uuid.UUID, pc *counter.PollCounters) error {
	accepted, rejectedDuplicate, rejectedRateLimited := pc.SwapDeltas()

	if !anyNonZero(accepted, rejectedDuplicate, rejectedRateLimited) {
		return nil
	}

	pipe := s.rdb.Pipeline()
	ck, rk := countsKey(pollID), rejectedKey(pollID)
	for i, delta := range accepted {
		if delta != 0 {
			pipe.HIncrBy(ctx, ck, strconv.Itoa(i+1), delta)
		}
	}
	if rejectedDuplicate != 0 {
		pipe.HIncrBy(ctx, rk, "duplicate", rejectedDuplicate)
	}
	if rejectedRateLimited != 0 {
		pipe.HIncrBy(ctx, rk, "rate_limited", rejectedRateLimited)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		pc.RestoreDeltas(accepted, rejectedDuplicate, rejectedRateLimited)
		return fmt.Errorf("flush poll %s counters: %w", pollID, err)
	}
	return nil
}

func anyNonZero(accepted []int64, a, b int64) bool {
	if a != 0 || b != 0 {
		return true
	}
	for _, d := range accepted {
		if d != 0 {
			return true
		}
	}
	return false
}

// FlushAll flushes every poll currently tracked by store. Errors are
// logged, not returned — one poll's Redis hiccup shouldn't stop the
// others from flushing this tick.
func (s *VoteCounterStore) FlushAll(ctx context.Context, store *counter.Store, logger *slog.Logger) {
	store.Range(func(pollID uuid.UUID, pc *counter.PollCounters) {
		if err := s.FlushPoll(ctx, pollID, pc); err != nil {
			logger.Error("flush poll counters", "poll_id", pollID, "error", err)
		}
	})
}

// Run flushes store into Redis once per interval until ctx is
// cancelled, then performs one final flush before returning — using a
// fresh, short-lived context, since ctx is already cancelled by then and
// would make that final flush's own Redis calls fail immediately. This
// is what makes "финальный флаш на shutdown" (docs/ai plan) actually
// happen: callers should wait for Run to return (e.g. via a
// sync.WaitGroup) before the process exits, or the final flush never
// gets the chance to run.
func (s *VoteCounterStore) Run(ctx context.Context, store *counter.Store, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			finalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			s.FlushAll(finalCtx, store, logger)
			cancel()
			return
		case <-ticker.C:
			s.FlushAll(ctx, store, logger)
		}
	}
}

// ReadCounts reads the currently-flushed counts for pollID — used by
// the snapshotter, which runs on its own cadence and doesn't share
// FlushPoll's in-memory deltas.
func (s *VoteCounterStore) ReadCounts(ctx context.Context, pollID uuid.UUID) (accepted map[int]int64, rejectedDuplicate, rejectedRateLimited int64, err error) {
	countsRaw, err := s.rdb.HGetAll(ctx, countsKey(pollID)).Result()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read counts: %w", err)
	}
	rejectedRaw, err := s.rdb.HGetAll(ctx, rejectedKey(pollID)).Result()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read rejected: %w", err)
	}

	accepted = make(map[int]int64, len(countsRaw))
	for field, val := range countsRaw {
		optionID, convErr := strconv.Atoi(field)
		if convErr != nil {
			continue // this hash only ever holds fields FlushPoll wrote
		}
		n, convErr := strconv.ParseInt(val, 10, 64)
		if convErr != nil {
			continue
		}
		accepted[optionID] = n
	}
	if v, ok := rejectedRaw["duplicate"]; ok {
		rejectedDuplicate, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := rejectedRaw["rate_limited"]; ok {
		rejectedRateLimited, _ = strconv.ParseInt(v, 10, 64)
	}
	return accepted, rejectedDuplicate, rejectedRateLimited, nil
}
