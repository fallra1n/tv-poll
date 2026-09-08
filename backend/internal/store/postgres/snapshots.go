package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SnapshotStore struct {
	db *pgxpool.Pool
}

func NewSnapshotStore(db *pgxpool.Pool) *SnapshotStore {
	return &SnapshotStore{db: db}
}

// SnapshotInput is one snapshotter tick's reading of a poll's Redis
// counters, before monotonicity is applied against the previous row.
type SnapshotInput struct {
	PollID              uuid.UUID
	At                  time.Time
	TotalAccepted       int64
	RejectedDuplicate   int64
	RejectedRateLimited int64
	OptionCounts        map[int]int64
}

type previousSnapshot struct {
	totalAccepted       int64
	rejectedDuplicate   int64
	rejectedRateLimited int64
	optionCounts        map[string]int64
}

// mergedSnapshot is what actually gets written: every counter floored
// at its previous value.
type mergedSnapshot struct {
	totalAccepted       int64
	rejectedDuplicate   int64
	rejectedRateLimited int64
	optionCounts        map[string]int64
}

// mergeSnapshot applies max(previous, next) to every counter — review
// finding B3: if Redis loses data (restart, eviction) between two
// snapshot ticks, the freshly-read value can be *lower* than what was
// already durably recorded. Writing that lower value straight through
// would make the votes-over-time chart show votes disappearing, which
// is a worse failure than a snapshot that's briefly stale. Kept as a
// pure function (no I/O) so this — the one property this step exists to
// guarantee — is unit-testable without a database.
func mergeSnapshot(prev previousSnapshot, next SnapshotInput) mergedSnapshot {
	merged := mergedSnapshot{
		totalAccepted:       max64(prev.totalAccepted, next.TotalAccepted),
		rejectedDuplicate:   max64(prev.rejectedDuplicate, next.RejectedDuplicate),
		rejectedRateLimited: max64(prev.rejectedRateLimited, next.RejectedRateLimited),
		optionCounts:        make(map[string]int64, len(next.OptionCounts)+len(prev.optionCounts)),
	}

	for optionID, count := range next.OptionCounts {
		key := strconv.Itoa(optionID)
		merged.optionCounts[key] = max64(prev.optionCounts[key], count)
	}
	// An option present in the previous row but missing from this read
	// (shouldn't happen — Redis never forgets a hash field on its own —
	// but the previous value is still the correct floor if it somehow
	// does) carries forward unchanged.
	for key, count := range prev.optionCounts {
		if _, ok := merged.optionCounts[key]; !ok {
			merged.optionCounts[key] = count
		}
	}

	return merged
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// InsertSnapshot writes one snapshot row for in.PollID, applying
// mergeSnapshot against the most recent existing row for that poll.
func (s *SnapshotStore) InsertSnapshot(ctx context.Context, in SnapshotInput) error {
	prev, err := s.loadPreviousSnapshot(ctx, in.PollID)
	if err != nil {
		return fmt.Errorf("load previous snapshot: %w", err)
	}

	merged := mergeSnapshot(prev, in)
	optionsJSON, err := json.Marshal(merged.optionCounts)
	if err != nil {
		return fmt.Errorf("marshal option_counts: %w", err)
	}

	_, err = s.db.Exec(ctx, `
		INSERT INTO poll_result_snapshots
			(poll_id, snapshot_at, total_accepted, rejected_duplicate, rejected_rate_limited, option_counts)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, in.PollID, in.At, merged.totalAccepted, merged.rejectedDuplicate, merged.rejectedRateLimited, optionsJSON)
	if err != nil {
		return fmt.Errorf("insert snapshot: %w", err)
	}
	return nil
}

func (s *SnapshotStore) loadPreviousSnapshot(ctx context.Context, pollID uuid.UUID) (previousSnapshot, error) {
	var (
		prev           previousSnapshot
		optionCountsJS []byte
	)
	err := s.db.QueryRow(ctx, `
		SELECT total_accepted, rejected_duplicate, rejected_rate_limited, option_counts
		FROM poll_result_snapshots
		WHERE poll_id = $1
		ORDER BY snapshot_at DESC
		LIMIT 1
	`, pollID).Scan(&prev.totalAccepted, &prev.rejectedDuplicate, &prev.rejectedRateLimited, &optionCountsJS)

	switch {
	case err == nil:
		if uerr := json.Unmarshal(optionCountsJS, &prev.optionCounts); uerr != nil {
			return previousSnapshot{}, fmt.Errorf("unmarshal previous option_counts: %w", uerr)
		}
		return prev, nil
	case errors.Is(err, pgx.ErrNoRows):
		// First snapshot for this poll — nothing to floor against.
		return previousSnapshot{optionCounts: map[string]int64{}}, nil
	default:
		return previousSnapshot{}, err
	}
}

// SnapshotRow is one row of poll_result_snapshots, decoded for read
// access (api/openapi.yaml's PollResults and ResultsSnapshotPoint).
// OptionCounts is keyed by option_id as a string, matching how it's
// stored in the option_counts JSONB column.
type SnapshotRow struct {
	SnapshotAt          time.Time
	TotalAccepted       int64
	RejectedDuplicate   int64
	RejectedRateLimited int64
	OptionCounts        map[string]int64
	SamplingEnabled     bool
	SamplingRate        *int
}

const snapshotRowSelect = `
	SELECT snapshot_at, total_accepted, rejected_duplicate, rejected_rate_limited,
	       option_counts, sampling_enabled, sampling_rate
	FROM poll_result_snapshots
`

func scanSnapshotRow(row scannable) (SnapshotRow, error) {
	var (
		r              SnapshotRow
		optionCountsJS []byte
	)
	if err := row.Scan(&r.SnapshotAt, &r.TotalAccepted, &r.RejectedDuplicate, &r.RejectedRateLimited,
		&optionCountsJS, &r.SamplingEnabled, &r.SamplingRate); err != nil {
		return SnapshotRow{}, err
	}
	if err := json.Unmarshal(optionCountsJS, &r.OptionCounts); err != nil {
		return SnapshotRow{}, fmt.Errorf("unmarshal option_counts: %w", err)
	}
	return r, nil
}

// LatestSnapshot returns the most recent snapshot for pollID, or
// ok=false if none exists yet — a poll that hasn't opened, or opened
// but the first snapshot tick (at most ~1s away) hasn't run yet.
// Callers render that as zeroed results with snapshot_at: null
// (api/openapi.yaml, PollResults), not as an error.
func (s *SnapshotStore) LatestSnapshot(ctx context.Context, pollID uuid.UUID) (SnapshotRow, bool, error) {
	row := s.db.QueryRow(ctx, snapshotRowSelect+" WHERE poll_id = $1 ORDER BY snapshot_at DESC LIMIT 1", pollID)
	r, err := scanSnapshotRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SnapshotRow{}, false, nil
		}
		return SnapshotRow{}, false, fmt.Errorf("load latest snapshot: %w", err)
	}
	return r, true, nil
}

// ListSnapshots returns every snapshot for pollID, oldest first — the
// votes-over-time chart (api/openapi.yaml: GET .../results/timeseries).
func (s *SnapshotStore) ListSnapshots(ctx context.Context, pollID uuid.UUID) ([]SnapshotRow, error) {
	rows, err := s.db.Query(ctx, snapshotRowSelect+" WHERE poll_id = $1 ORDER BY snapshot_at ASC", pollID)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	defer rows.Close()

	var result []SnapshotRow
	for rows.Next() {
		r, err := scanSnapshotRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
