package postgres_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/fallra1n/tvpoll/internal/store/postgres"
	"github.com/fallra1n/tvpoll/migrations"
)

// newTestPostgres starts a real Postgres 16 container, applies the same
// goose migrations cmd/migrate runs in production, and returns a pool —
// so integration tests exercise the actual schema (triggers, checks),
// not a hand-approximated one.
func newTestPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping testcontainers test in -short mode")
	}
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("tvpoll"),
		tcpostgres.WithUsername("tvpoll"),
		tcpostgres.WithPassword("tvpoll"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	})

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	sqlDB, err := sql.Open("pgx", connStr)
	if err != nil {
		t.Fatalf("open database/sql: %v", err)
	}
	defer sqlDB.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose set dialect: %v", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		t.Fatalf("goose up: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("open pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// insertTestPoll inserts a minimal open poll directly (bypassing
// PollStore.CreatePoll/TransitionPoll — this test only needs a poll_id
// that satisfies the poll_result_snapshots foreign key, not a poll
// exercised through its own lifecycle, which internal/httpapi's
// end-to-end tests already cover).
func insertTestPoll(t *testing.T, db *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := db.QueryRow(context.Background(), `
		INSERT INTO polls (question, state, scheduled_at, voting_window_seconds)
		VALUES ('test poll', 'open', now(), 300)
		RETURNING id
	`).Scan(&id)
	if err != nil {
		t.Fatalf("insert test poll: %v", err)
	}
	return id
}

func TestSnapshotStore_InsertSnapshot_FirstSnapshot(t *testing.T) {
	db := newTestPostgres(t)
	store := postgres.NewSnapshotStore(db)
	pollID := insertTestPoll(t, db)

	err := store.InsertSnapshot(context.Background(), postgres.SnapshotInput{
		PollID:              pollID,
		At:                  time.Now(),
		TotalAccepted:       5,
		RejectedDuplicate:   1,
		RejectedRateLimited: 0,
		OptionCounts:        map[int]int64{1: 3, 2: 2},
	})
	if err != nil {
		t.Fatalf("InsertSnapshot: %v", err)
	}

	var total, rejDup int64
	err = db.QueryRow(context.Background(),
		`SELECT total_accepted, rejected_duplicate FROM poll_result_snapshots WHERE poll_id = $1`,
		pollID).Scan(&total, &rejDup)
	if err != nil {
		t.Fatalf("read back snapshot: %v", err)
	}
	if total != 5 || rejDup != 1 {
		t.Errorf("total=%d rejDup=%d, want 5 and 1", total, rejDup)
	}
}

// TestSnapshotStore_InsertSnapshot_Monotonicity is the real end-to-end
// proof of review finding B3, against an actual Postgres table rather
// than the pure mergeSnapshot function alone: a snapshot with a lower
// total than the previous one (simulating Redis having lost data
// between two ticks) must not make the stored value go backwards.
func TestSnapshotStore_InsertSnapshot_Monotonicity(t *testing.T) {
	db := newTestPostgres(t)
	store := postgres.NewSnapshotStore(db)
	pollID := insertTestPoll(t, db)
	ctx := context.Background()

	if err := store.InsertSnapshot(ctx, postgres.SnapshotInput{
		PollID: pollID, At: time.Now(), TotalAccepted: 100,
		OptionCounts: map[int]int64{1: 60, 2: 40},
	}); err != nil {
		t.Fatalf("first InsertSnapshot: %v", err)
	}

	// Second tick, one moment later, reads a *lower* value from Redis —
	// the scenario B3 exists to handle.
	if err := store.InsertSnapshot(ctx, postgres.SnapshotInput{
		PollID: pollID, At: time.Now().Add(time.Second), TotalAccepted: 7,
		OptionCounts: map[int]int64{1: 5, 2: 2},
	}); err != nil {
		t.Fatalf("second InsertSnapshot: %v", err)
	}

	var total int64
	var optionCountsRaw []byte
	err := db.QueryRow(ctx, `
		SELECT total_accepted, option_counts FROM poll_result_snapshots
		WHERE poll_id = $1 ORDER BY snapshot_at DESC LIMIT 1
	`, pollID).Scan(&total, &optionCountsRaw)
	if err != nil {
		t.Fatalf("read back latest snapshot: %v", err)
	}
	if total != 100 {
		t.Errorf("latest total_accepted = %d, want 100 (the regression must be floored, not stored)", total)
	}

	var rows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM poll_result_snapshots WHERE poll_id = $1`, pollID).Scan(&rows); err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	if rows != 2 {
		t.Errorf("expected 2 snapshot rows (one per tick, monotonicity affects values not row count), got %d", rows)
	}
}
