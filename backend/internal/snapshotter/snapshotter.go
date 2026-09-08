// Package snapshotter periodically drains Redis's accumulated vote
// counters into Postgres (docs/ai/02-load-model.md §6.6: results are
// eventually consistent, a snapshot once a second is enough — nobody
// needs the exact live count, and the snapshot history doubles as the
// votes-over-time chart).
package snapshotter

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/fallra1n/tvpoll/internal/leader"
	"github.com/fallra1n/tvpoll/internal/store/postgres"
	"github.com/fallra1n/tvpoll/internal/store/redisstore"
)

// closeGrace is how long past closed_at a poll keeps being snapshotted
// — review finding B2: without this, votes accepted in the last second
// before close could arrive after the tick that would have captured
// them, and never make it into a snapshot.
const closeGrace = 5 * time.Second

// Snapshotter runs only on the elected leader (docs/ai review B1 —
// otherwise every instance would write a snapshot row every second,
// multiplying the reported vote counts by the instance count) and,
// while leader, snapshots every currently relevant poll once per tick.
type Snapshotter struct {
	elector   *leader.Elector
	polls     *postgres.PollStore
	snapshots *postgres.SnapshotStore
	counters  *redisstore.VoteCounterStore
	logger    *slog.Logger
}

func New(elector *leader.Elector, polls *postgres.PollStore, snapshots *postgres.SnapshotStore, counters *redisstore.VoteCounterStore, logger *slog.Logger) *Snapshotter {
	return &Snapshotter{
		elector:   elector,
		polls:     polls,
		snapshots: snapshots,
		counters:  counters,
		logger:    logger,
	}
}

// Run ticks once per interval until ctx is cancelled. No final-flush
// synchronization is needed on shutdown (unlike the counter flusher):
// this reads from Redis and writes to Postgres, holding no client-side
// state that would be lost by stopping mid-cycle — whichever instance
// becomes leader next simply resumes on the next tick.
func (s *Snapshotter) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Snapshotter) tick(ctx context.Context) {
	isLeader, err := s.elector.IsLeader(ctx)
	if err != nil {
		s.logger.Error("snapshotter: leader election", "error", err)
		return
	}
	if !isLeader {
		return
	}

	now := time.Now()
	ids, err := s.polls.ListSnapshotCandidateIDs(ctx, now.Add(-closeGrace))
	if err != nil {
		s.logger.Error("snapshotter: list candidates", "error", err)
		return
	}

	for _, id := range ids {
		if err := s.snapshotOne(ctx, id, now); err != nil {
			s.logger.Error("snapshotter: snapshot poll", "poll_id", id, "error", err)
		}
	}
}

func (s *Snapshotter) snapshotOne(ctx context.Context, pollID uuid.UUID, at time.Time) error {
	accepted, rejectedDuplicate, rejectedRateLimited, err := s.counters.ReadCounts(ctx, pollID)
	if err != nil {
		return err
	}

	var total int64
	for _, n := range accepted {
		total += n
	}

	return s.snapshots.InsertSnapshot(ctx, postgres.SnapshotInput{
		PollID:              pollID,
		At:                  at,
		TotalAccepted:       total,
		RejectedDuplicate:   rejectedDuplicate,
		RejectedRateLimited: rejectedRateLimited,
		OptionCounts:        accepted,
	})
}
