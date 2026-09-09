package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fallra1n/tvpoll/internal/store/postgres"
)

func TestPollStoreListExpiredOpenPolls(t *testing.T) {
	db := newTestPostgres(t)
	store := postgres.NewPollStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	insert := func(state string, scheduledAt time.Time, windowSeconds int) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := db.QueryRow(ctx, `
			INSERT INTO polls (question, state, scheduled_at, voting_window_seconds)
			VALUES ('scheduler test', $1, $2, $3)
			RETURNING id
		`, state, scheduledAt, windowSeconds).Scan(&id); err != nil {
			t.Fatalf("insert %s poll: %v", state, err)
		}
		return id
	}

	expiredID := insert("open", now.Add(-2*time.Minute), 30)
	insert("open", now, 300)
	insert("scheduled", now.Add(-2*time.Minute), 30)
	insert("closed", now.Add(-2*time.Minute), 30)

	ids, err := store.ListExpiredOpenPolls(ctx, now)
	if err != nil {
		t.Fatalf("ListExpiredOpenPolls: %v", err)
	}
	if len(ids) != 1 || ids[0] != expiredID {
		t.Fatalf("expired ids = %v, want [%s]", ids, expiredID)
	}
}
