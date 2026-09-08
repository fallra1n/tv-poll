package cache

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/fallra1n/tvpoll/internal/domain"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeLoader is an in-memory stand-in for *postgres.PollStore, so the
// cache's behavior can be tested without a database.
type fakeLoader struct {
	mu    sync.Mutex
	byID  map[uuid.UUID]domain.Poll
	calls int // GetByID call count, to assert fetch-through only fires once per miss
}

func (f *fakeLoader) GetByID(_ context.Context, id uuid.UUID) (domain.Poll, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	p, ok := f.byID[id]
	if !ok {
		return domain.Poll{}, pgx.ErrNoRows
	}
	return p, nil
}

func (f *fakeLoader) ListByStateOrID(_ context.Context, states []domain.PollState, ids []uuid.UUID) ([]domain.Poll, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	wantState := make(map[domain.PollState]bool, len(states))
	for _, s := range states {
		wantState[s] = true
	}
	wantID := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		wantID[id] = true
	}

	var out []domain.Poll
	for _, p := range f.byID {
		if wantState[p.State] || wantID[p.ID] {
			out = append(out, p)
		}
	}
	return out, nil
}

func TestCache_Get_FetchThrough(t *testing.T) {
	id := uuid.New()
	poll := domain.Poll{
		ID: id, Question: "Q", State: domain.PollOpen,
		Options: []domain.PollOption{{Ordinal: 1, Label: "A"}},
	}
	loader := &fakeLoader{byID: map[uuid.UUID]domain.Poll{id: poll}}
	c := New(loader, testLogger())

	if _, ok := c.GetCached(id); ok {
		t.Fatal("expected a cache miss before the first Get")
	}

	d, err := c.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Question != "Q" || d.OptionCount() != 1 {
		t.Fatalf("unexpected definition: %+v", d)
	}

	if _, ok := c.GetCached(id); !ok {
		t.Fatal("expected a cache hit after the fetch-through")
	}
	if loader.calls != 1 {
		t.Fatalf("expected exactly 1 GetByID call, got %d", loader.calls)
	}

	// A second Get must be served from cache, not hit the loader again.
	if _, err := c.Get(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if loader.calls != 1 {
		t.Fatalf("expected still 1 GetByID call after a cached Get, got %d", loader.calls)
	}
}

// TestCache_Get_DoesNotCacheDraft guards against a real bug found while
// exercising this endpoint end-to-end: a draft poll's fetch-through
// result must not be cached, because a draft poll never matches the
// background refresh's state filter — a cached draft entry would never
// get corrected once the poll is actually scheduled, permanently
// shadowing it with a stale 404.
func TestCache_Get_DoesNotCacheDraft(t *testing.T) {
	id := uuid.New()
	loader := &fakeLoader{byID: map[uuid.UUID]domain.Poll{
		id: {ID: id, Question: "Q", State: domain.PollDraft},
	}}
	c := New(loader, testLogger())

	if _, err := c.Get(context.Background(), id); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := c.GetCached(id); ok {
		t.Fatal("a draft poll must not be cached — it would never be corrected once scheduled")
	}

	// The poll gets scheduled "in the database" between the two Gets.
	scheduled := loader.byID[id]
	scheduled.State = domain.PollScheduled
	loader.byID[id] = scheduled

	d, err := c.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.State != domain.PollScheduled {
		t.Fatalf("expected the fresh fetch to see the scheduled state, got %s", d.State)
	}
	if loader.calls != 2 {
		t.Fatalf("expected 2 GetByID calls (draft wasn't cached, so the second Get re-fetches), got %d", loader.calls)
	}
}

func TestCache_Get_NotFound(t *testing.T) {
	loader := &fakeLoader{byID: map[uuid.UUID]domain.Poll{}}
	c := New(loader, testLogger())

	_, err := c.Get(context.Background(), uuid.New())
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected pgx.ErrNoRows, got %v", err)
	}
}

// TestCache_Refresh_TracksTransitionToClosed is the property the
// package doc comment promises: a poll that closes while cached is
// caught by exactly one more refresh (it was "active" as of the
// previous tick), after which it drops out of the actively-refreshed
// set — but its last known (closed) value stays cached, not evicted and
// not re-queried forever.
func TestCache_Refresh_TracksTransitionToClosed(t *testing.T) {
	id := uuid.New()
	openPoll := domain.Poll{ID: id, State: domain.PollOpen}
	loader := &fakeLoader{byID: map[uuid.UUID]domain.Poll{id: openPoll}}
	c := New(loader, testLogger())

	// Tick 1: poll is open, discovered via the state filter, tracked active.
	if err := c.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, ok := c.GetCached(id)
	if !ok || d.State != domain.PollOpen {
		t.Fatalf("expected open poll cached after tick 1, got %+v ok=%v", d, ok)
	}
	if _, active := c.active[id]; !active {
		t.Fatal("expected poll to be tracked active after tick 1")
	}

	// The poll closes "in the database" between ticks.
	closedPoll := openPoll
	closedPoll.State = domain.PollClosed
	loader.byID[id] = closedPoll

	// Tick 2: no longer matches the state filter, but is still re-fetched
	// because it was active as of tick 1 — this is the one grace tick.
	if err := c.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, ok = c.GetCached(id)
	if !ok || d.State != domain.PollClosed {
		t.Fatalf("expected cached state corrected to closed after tick 2, got %+v ok=%v", d, ok)
	}
	if _, active := c.active[id]; active {
		t.Fatal("expected poll to drop out of the active set once closed")
	}

	// Remove it from the "database" entirely: if tick 3 queried it again
	// (it shouldn't — it's neither scheduled/open nor in `active`
	// anymore), GetByID/ListByStateOrID simply wouldn't return it, and a
	// buggy refresh that dropped stale entries would lose it here.
	delete(loader.byID, id)

	if err := c.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, ok = c.GetCached(id)
	if !ok || d.State != domain.PollClosed {
		t.Fatalf("expected closed poll to remain cached at its last known value, got %+v ok=%v", d, ok)
	}
}

func TestPollDefinition_OptionCount(t *testing.T) {
	d := PollDefinition{Options: []domain.PollOption{{Ordinal: 1, Label: "a"}, {Ordinal: 2, Label: "b"}}}
	if got := d.OptionCount(); got != 2 {
		t.Errorf("OptionCount() = %d, want 2", got)
	}
}
