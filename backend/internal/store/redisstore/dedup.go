// Package redisstore holds the vote hot path's single Redis
// interaction: marking a vote token's nonce as used.
package redisstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// DedupStore marks vote tokens as used — the one Redis round-trip on the
// vote hot path (docs/ai/02-load-model.md §4.2). Correctness comes from
// Redis's own SET ... NX atomicity, not from any client-side locking or
// a Lua script: concurrent claims of the same key race at the server,
// and exactly one wins.
type DedupStore struct {
	rdb *redis.Client
}

func NewDedupStore(rdb *redis.Client) *DedupStore {
	return &DedupStore{rdb: rdb}
}

// TryClaim marks nonce as used for pollID, storing optionID as the
// value via `SET key optionID NX GET EX ttl`. If this is the first
// claim, Redis's GET returns nil (nothing existed yet) and this returns
// (0, true, nil) — vote accepted. If nonce was already claimed, GET
// returns the value stored by that earlier claim without overwriting
// it, and this returns (thatOptionID, false, nil) — the caller can
// report which option the earlier, successful vote was for, instead of
// a bare "duplicate" (review finding C5: a client that retries after a
// lost response shouldn't be told something went wrong).
//
// ttl must be closes_at - now + grace (docs/ai/03-deduplication.md
// §2.3); the caller computes it, since this package doesn't know about
// polls.
func (s *DedupStore) TryClaim(ctx context.Context, pollID uuid.UUID, nonce string, optionID int, ttl time.Duration) (previousOptionID int, accepted bool, err error) {
	key := dedupKey(pollID, nonce)

	prev, err := s.rdb.SetArgs(ctx, key, strconv.Itoa(optionID), redis.SetArgs{
		Mode: "NX",
		Get:  true,
		TTL:  ttl,
	}).Result()

	switch {
	case errors.Is(err, redis.Nil):
		// No previous value: the NX condition was met, our write went
		// through — first (accepted) claim.
		return 0, true, nil
	case err != nil:
		return 0, false, fmt.Errorf("claim dedup key: %w", err)
	}

	prevOptionID, convErr := strconv.Atoi(prev)
	if convErr != nil {
		// A value exists but isn't the small integer this method always
		// writes — shouldn't happen (this key only ever holds what
		// TryClaim wrote), so surface it as a real error, not a guess.
		return 0, false, fmt.Errorf("dedup key held unexpected value %q: %w", prev, convErr)
	}
	return prevOptionID, false, nil
}

func dedupKey(pollID uuid.UUID, nonce string) string {
	return fmt.Sprintf("d:%s:%s", pollID, nonce)
}
