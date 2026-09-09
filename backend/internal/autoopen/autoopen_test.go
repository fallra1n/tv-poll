package autoopen

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fallra1n/tvpoll/internal/domain"
)

type fakeElector struct {
	leader bool
	calls  int
}

func (e *fakeElector) IsLeader(context.Context) (bool, error) {
	e.calls++
	return e.leader, nil
}

type transitionCall struct {
	id uuid.UUID
	to domain.PollState
}

type fakePollStore struct {
	due         []uuid.UUID
	expired     []uuid.UUID
	transitions []transitionCall
}

func (s *fakePollStore) ListDuePolls(context.Context, time.Time) ([]uuid.UUID, error) {
	return s.due, nil
}

func (s *fakePollStore) ListExpiredOpenPolls(context.Context, time.Time) ([]uuid.UUID, error) {
	return s.expired, nil
}

func (s *fakePollStore) TransitionPoll(_ context.Context, id uuid.UUID, req domain.TransitionRequest) (domain.Poll, error) {
	s.transitions = append(s.transitions, transitionCall{id: id, to: req.To})
	return domain.Poll{ID: id, State: req.To}, nil
}

type fakePollCache struct {
	polls []domain.Poll
}

func (c *fakePollCache) Put(p domain.Poll) {
	c.polls = append(c.polls, p)
}

func TestTickDoesNothingWhenNotLeader(t *testing.T) {
	elector := &fakeElector{}
	store := &fakePollStore{due: []uuid.UUID{uuid.New()}, expired: []uuid.UUID{uuid.New()}}
	cache := &fakePollCache{}
	opener := &AutoOpener{
		elector:   elector,
		polls:     store,
		pollCache: cache,
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	opener.tick(context.Background())

	if len(store.transitions) != 0 || len(cache.polls) != 0 {
		t.Fatal("non-leader applied scheduled transitions")
	}
}

func TestTickOpensAndClosesDuePolls(t *testing.T) {
	openID := uuid.New()
	closeID := uuid.New()
	store := &fakePollStore{due: []uuid.UUID{openID}, expired: []uuid.UUID{closeID}}
	cache := &fakePollCache{}
	opener := &AutoOpener{
		elector:   &fakeElector{leader: true},
		polls:     store,
		pollCache: cache,
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	opener.tick(context.Background())

	want := []transitionCall{
		{id: openID, to: domain.PollOpen},
		{id: closeID, to: domain.PollClosed},
	}
	if len(store.transitions) != len(want) {
		t.Fatalf("got %d transitions, want %d", len(store.transitions), len(want))
	}
	for i := range want {
		if store.transitions[i] != want[i] {
			t.Fatalf("transition %d = %+v, want %+v", i, store.transitions[i], want[i])
		}
	}
	if len(cache.polls) != 2 || cache.polls[0].State != domain.PollOpen || cache.polls[1].State != domain.PollClosed {
		t.Fatalf("unexpected cache updates: %+v", cache.polls)
	}
}
