// Package cache holds an in-process, eventually-consistent view of poll
// definitions — refreshed from Postgres in the background so that
// neither the public GET nor (critically) the vote hot path ever touch
// Postgres directly (docs/ai/02-load-model.md §4.2: the vote handler is
// "разбор JSON → проверка HMAC → один round-trip в Redis" — no disk
// access at all).
package cache

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/fallra1n/tvpoll/internal/domain"
)

// PollDefinition is the subset of a poll needed to answer the public GET
// and to accept a vote.
type PollDefinition struct {
	ID          uuid.UUID
	Question    string
	Options     []domain.PollOption
	State       domain.PollState
	ScheduledAt *time.Time
	ClosesAt    *time.Time
}

// OptionCount is used on the vote path to reject an out-of-range
// option_id without a Postgres round-trip.
func (d PollDefinition) OptionCount() int {
	return len(d.Options)
}

func fromPoll(p domain.Poll) PollDefinition {
	return PollDefinition{
		ID:          p.ID,
		Question:    p.Question,
		Options:     p.Options,
		State:       p.State,
		ScheduledAt: p.ScheduledAt,
		ClosesAt:    p.ClosesAt,
	}
}

// Loader is the subset of *postgres.PollStore the cache needs, kept as
// an interface so the cache can be unit-tested with a fake instead of a
// database.
type Loader interface {
	GetByID(ctx context.Context, id uuid.UUID) (domain.Poll, error)
	ListByStateOrID(ctx context.Context, states []domain.PollState, ids []uuid.UUID) ([]domain.Poll, error)
}

// Cache holds poll definitions for fast, Postgres-free reads.
//
// Population has two paths:
//   - proactive: Run's background loop refreshes every scheduled/open
//     poll once a second, so a poll is warm well before its voting
//     window opens — no request ever waits on the first refresh.
//   - fetch-through: Get falls back to a direct Postgres read on a
//     miss, so GET /v1/polls/{id} keeps working for old/closed polls
//     without keeping every poll ever created in the actively-refreshed
//     set forever.
//
// A poll that transitions scheduled/open -> closed is caught by exactly
// one more refresh tick — it was active as of the previous tick, so it's
// re-fetched once more (see `active` below), its cached state corrected
// to closed, and then it drops out of the actively-refreshed set. Until
// that tick runs, a vote against it can still be accepted from the stale
// cached state: an accepted, documented staleness window of at most the
// refresh interval, matching "до 1с приёма голосов после досрочного
// ручного закрытия" from the plan — the safe side per R14 (a false
// rejection of a real voter is worse than a few extra accepted votes).
type Cache struct {
	loader Loader
	logger *slog.Logger

	mu     sync.RWMutex
	data   map[uuid.UUID]PollDefinition
	active map[uuid.UUID]struct{}
}

func New(loader Loader, logger *slog.Logger) *Cache {
	return &Cache{
		loader: loader,
		logger: logger,
		data:   make(map[uuid.UUID]PollDefinition),
		active: make(map[uuid.UUID]struct{}),
	}
}

// Get returns a poll definition, fetching it from Postgres on a cache
// miss (and caching the result for next time). Used by GET
// /v1/polls/{id} — not by the vote hot path, which uses GetCached
// instead and must never wait on Postgres.
func (c *Cache) Get(ctx context.Context, id uuid.UUID) (PollDefinition, error) {
	if d, ok := c.GetCached(id); ok {
		return d, nil
	}

	p, err := c.loader.GetByID(ctx, id)
	if err != nil {
		return PollDefinition{}, err
	}
	d := fromPoll(p)

	// A draft poll is not immutable yet — it isn't in the background
	// refresh's state filter (only scheduled/open are), so caching it
	// here would never get corrected once it's actually scheduled: the
	// next Get would keep serving this stale draft entry forever. Simplest
	// correct fix: never cache draft, so every draft lookup re-reads
	// Postgres (rare and cheap — a poll spends at most a brief window in
	// draft, and a draft poll returns 404 from the HTTP layer regardless,
	// so there's no repeat CDN traffic hitting this path).
	if d.State != domain.PollDraft {
		c.store(d)
	}
	return d, nil
}

// GetCached reads only the in-memory map — zero I/O, safe for the vote
// hot path.
func (c *Cache) GetCached(id uuid.UUID) (PollDefinition, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	d, ok := c.data[id]
	return d, ok
}

func (c *Cache) store(d PollDefinition) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[d.ID] = d
}

// Put makes p's latest known state visible to cache reads immediately,
// without waiting for the next background refresh tick. Callers that
// just wrote a state transition to Postgres (admin create/transition
// handlers) call this right after — found necessary while exercising
// the vote flow end-to-end: without it, a vote arriving in the up-to-
// one-second gap between an admin's scheduled -> open transition and
// the next refresh tick would see the stale cached "scheduled" state
// and get rejected as "closed" — a false rejection of a real voter,
// which is the wrong direction of error per R14 (docs/ai/00-task-
// original.md) — the background refresh already accepts the opposite
// staleness (accepting votes for up to one tick after a close) as the
// correct side to err on; this closes the other direction.
//
// A poll that's scheduled/open is added to `active` exactly as refresh
// would, so the grace-tick mechanism keeps tracking it into whatever
// state it transitions to next, even though refresh didn't discover it
// itself this time.
func (c *Cache) Put(p domain.Poll) {
	d := fromPoll(p)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[d.ID] = d
	if d.State == domain.PollScheduled || d.State == domain.PollOpen {
		c.active[d.ID] = struct{}{}
	} else {
		delete(c.active, d.ID)
	}
}

// Run refreshes scheduled/open polls, plus whatever was scheduled/open
// as of the previous tick (to catch its transition to closed), once per
// interval until ctx is cancelled. Call this in a goroutine at startup.
func (c *Cache) Run(ctx context.Context, interval time.Duration) {
	if err := c.refresh(ctx); err != nil {
		c.logger.Error("poll cache initial refresh failed", "error", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.refresh(ctx); err != nil {
				c.logger.Error("poll cache refresh failed", "error", err)
			}
		}
	}
}

func (c *Cache) refresh(ctx context.Context) error {
	c.mu.RLock()
	prevActive := make([]uuid.UUID, 0, len(c.active))
	for id := range c.active {
		prevActive = append(prevActive, id)
	}
	c.mu.RUnlock()

	polls, err := c.loader.ListByStateOrID(ctx,
		[]domain.PollState{domain.PollScheduled, domain.PollOpen}, prevActive)
	if err != nil {
		return err
	}

	newActive := make(map[uuid.UUID]struct{}, len(polls))
	fresh := make(map[uuid.UUID]PollDefinition, len(polls))
	for _, p := range polls {
		fresh[p.ID] = fromPoll(p)
		if p.State == domain.PollScheduled || p.State == domain.PollOpen {
			newActive[p.ID] = struct{}{}
		}
	}

	c.mu.Lock()
	for id, d := range fresh {
		c.data[id] = d
	}
	c.active = newActive
	c.mu.Unlock()
	return nil
}
