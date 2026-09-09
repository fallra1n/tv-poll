// Package autoopen applies schedule-driven poll transitions: scheduled polls
// open when scheduled_at arrives and open polls close at closes_at.
package autoopen

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/fallra1n/tvpoll/internal/domain"
)

type elector interface {
	IsLeader(context.Context) (bool, error)
}

type pollStore interface {
	ListDuePolls(context.Context, time.Time) ([]uuid.UUID, error)
	ListExpiredOpenPolls(context.Context, time.Time) ([]uuid.UUID, error)
	TransitionPoll(context.Context, uuid.UUID, domain.TransitionRequest) (domain.Poll, error)
}

type pollCache interface {
	Put(domain.Poll)
}

// AutoOpener runs only on the elected leader so replicas do not race through
// the same scheduled transitions on every tick.
type AutoOpener struct {
	elector   elector
	polls     pollStore
	pollCache pollCache
	logger    *slog.Logger
}

func New(elector elector, polls pollStore, cacheWriter pollCache, logger *slog.Logger) *AutoOpener {
	return &AutoOpener{elector: elector, polls: polls, pollCache: cacheWriter, logger: logger}
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

	now := time.Now()
	o.openDue(ctx, now)
	o.closeExpired(ctx, now)
}

func (o *AutoOpener) openDue(ctx context.Context, now time.Time) {
	ids, err := o.polls.ListDuePolls(ctx, now)
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

func (o *AutoOpener) closeExpired(ctx context.Context, now time.Time) {
	ids, err := o.polls.ListExpiredOpenPolls(ctx, now)
	if err != nil {
		o.logger.Error("autoclose: list expired polls", "error", err)
		return
	}

	for _, id := range ids {
		poll, err := o.polls.TransitionPoll(ctx, id, domain.TransitionRequest{To: domain.PollClosed})
		if err != nil {
			var transErr *domain.TransitionError
			if errors.As(err, &transErr) {
				continue
			}
			o.logger.Error("autoclose: transition poll", "poll_id", id, "error", err)
			continue
		}
		o.pollCache.Put(poll)
		o.logger.Info("autoclose: closed poll", "poll_id", id)
	}
}
