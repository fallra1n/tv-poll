// Package autoopen transitions polls from scheduled to open the moment
// their scheduled_at arrives (review finding B4) — without this, a
// human has to click "open" in the exact second the TV spot airs, which
// defeats the entire point of docs/ai/02-load-model.md §6.5's
// pre-scaling-by-schedule design: the peak is known to the second, but
// only if something actually opens the poll on that second.
package autoopen

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/fallra1n/tvpoll/internal/cache"
	"github.com/fallra1n/tvpoll/internal/domain"
	"github.com/fallra1n/tvpoll/internal/leader"
	"github.com/fallra1n/tvpoll/internal/store/postgres"
)

// AutoOpener runs only on the elected leader, same reasoning as
// internal/snapshotter — otherwise every instance would race to open
// the same poll every tick (harmless but noisy: TransitionPoll's
// SELECT ... FOR UPDATE means only one would ever win).
type AutoOpener struct {
	elector   *leader.Elector
	polls     *postgres.PollStore
	pollCache *cache.Cache
	logger    *slog.Logger
}

func New(elector *leader.Elector, polls *postgres.PollStore, pollCache *cache.Cache, logger *slog.Logger) *AutoOpener {
	return &AutoOpener{elector: elector, polls: polls, pollCache: pollCache, logger: logger}
}

func (o *AutoOpener) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.tick(ctx)
		}
	}
}

func (o *AutoOpener) tick(ctx context.Context) {
	isLeader, err := o.elector.IsLeader(ctx)
	if err != nil {
		o.logger.Error("autoopen: leader election", "error", err)
		return
	}
	if !isLeader {
		return
	}

	ids, err := o.polls.ListDuePolls(ctx, time.Now())
	if err != nil {
		o.logger.Error("autoopen: list due polls", "error", err)
		return
	}

	for _, id := range ids {
		poll, err := o.polls.TransitionPoll(ctx, id, domain.TransitionRequest{To: domain.PollOpen})
		if err != nil {
			var transErr *domain.TransitionError
			if errors.As(err, &transErr) {
				// Already opened — manually, or by a leader that changed
				// between our list query and this transition. Not an
				// error, just a race we lost harmlessly (the other path
				// already did the Put below).
				continue
			}
			o.logger.Error("autoopen: transition poll", "poll_id", id, "error", err)
			continue
		}
		// Same reason as the admin transition handler's Put call: must be
		// visible to the vote/token cache immediately, not after the next
		// 1s refresh tick, or a vote arriving right at air time could see
		// stale "scheduled" state and be wrongly rejected as closed.
		o.pollCache.Put(poll)
		o.logger.Info("autoopen: opened poll", "poll_id", id)
	}
}
