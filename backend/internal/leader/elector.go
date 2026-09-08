// Package leader provides simple, best-effort leader election over a
// single Redis key — good enough for "one instance should run this
// periodic job" (the snapshotter, the auto-opener), not a rigorous
// distributed lock. See Elector.IsLeader's doc comment for the accepted
// race window and why it's fine for these jobs specifically.
package leader

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Elector holds one Redis-backed lease. Every background job in this
// process that needs "only one instance should do this" shares the same
// Elector (and therefore the same lease key) — there's one coordinator
// role per process fleet, not one election per job, so instances don't
// end up split across jobs in confusing ways.
type Elector struct {
	rdb        *redis.Client
	key        string
	lease      time.Duration
	instanceID string
}

func New(rdb *redis.Client, key string, lease time.Duration) *Elector {
	return &Elector{rdb: rdb, key: key, lease: lease, instanceID: uuid.NewString()}
}

// IsLeader tries to become leader if nobody currently is, or renews the
// lease if this instance already is.
//
// Not a rigorous distributed lock — there's a narrow window between the
// ownership check and the renewal where leadership could theoretically
// change hands. Deliberately so: every job that consults this expects to
// run "roughly once, by roughly one instance" against operations that
// are already safe if that's violated for a tick — a periodic snapshot
// INSERT (monotonicity-protected, see internal/store/postgres's
// mergeSnapshot) or a poll state transition (already protected by
// SELECT ... FOR UPDATE in PollStore.TransitionPoll, so a second
// "leader" attempting the same transition just gets a harmless
// TransitionError). The worst case is one tick with zero or two
// leaders, not a correctness incident — not worth a Lua-based CAS for.
func (e *Elector) IsLeader(ctx context.Context) (bool, error) {
	if _, err := e.rdb.SetNX(ctx, e.key, e.instanceID, e.lease).Result(); err != nil {
		return false, err
	}

	current, err := e.rdb.Get(ctx, e.key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			// Lease disappeared between SetNX and Get (e.g. it expired
			// right on the boundary) — try again next tick.
			return false, nil
		}
		return false, err
	}
	if current != e.instanceID {
		return false, nil
	}

	if err := e.rdb.Expire(ctx, e.key, e.lease).Err(); err != nil {
		return false, err
	}
	return true, nil
}
