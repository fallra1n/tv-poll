// Package postgres holds hand-written SQL against pgx/v5 (pgxpool) — no
// ORM/codegen, per docs/ai/01-stack.md ("схема ~4-5 таблиц").
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fallra1n/tvpoll/internal/domain"
)

type PollStore struct {
	db *pgxpool.Pool
}

func NewPollStore(db *pgxpool.Pool) *PollStore {
	return &PollStore{db: db}
}

// CreatePoll inserts the poll, its options, and an audit-log entry in one
// transaction. A poll is created `scheduled` if ScheduledAt is set,
// `draft` otherwise (api/openapi.yaml: PollCreateRequest.scheduled_at
// "Если задано — опрос сразу создаётся в scheduled, а не draft").
func (s *PollStore) CreatePoll(ctx context.Context, in domain.CreatePollInput) (domain.Poll, error) {
	state := domain.PollDraft
	if in.ScheduledAt != nil {
		state = domain.PollScheduled
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return domain.Poll{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	var p domain.Poll
	err = tx.QueryRow(ctx, `
		INSERT INTO polls (question, state, scheduled_at, voting_window_seconds)
		VALUES ($1, $2, $3, $4)
		RETURNING id, question, state, scheduled_at, voting_window_seconds,
		          closes_at, opened_at, closed_at, created_at, updated_at
	`, in.Question, state, in.ScheduledAt, in.VotingWindowSeconds).Scan(
		&p.ID, &p.Question, &p.State, &p.ScheduledAt, &p.VotingWindowSeconds,
		&p.ClosesAt, &p.OpenedAt, &p.ClosedAt, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return domain.Poll{}, fmt.Errorf("insert poll: %w", err)
	}

	// One bulk insert via unnest+ordinality instead of N round-trips —
	// options come back in submitted order as ordinal 1..N, which is
	// also the wire option_id (docs/ai/04-data-model.md §1.2).
	_, err = tx.Exec(ctx, `
		INSERT INTO poll_options (poll_id, ordinal, label)
		SELECT $1, ordinality::smallint, label
		FROM unnest($2::text[]) WITH ORDINALITY AS t(label, ordinality)
	`, p.ID, in.Options)
	if err != nil {
		return domain.Poll{}, fmt.Errorf("insert options: %w", err)
	}

	p.Options = make([]domain.PollOption, len(in.Options))
	for i, label := range in.Options {
		p.Options[i] = domain.PollOption{Ordinal: i + 1, Label: label}
	}

	detail := map[string]any{
		"question":     p.Question,
		"option_count": len(p.Options),
	}
	if err := insertAuditLog(ctx, tx, "poll.create", &p.ID, detail); err != nil {
		return domain.Poll{}, fmt.Errorf("audit log: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Poll{}, fmt.Errorf("commit tx: %w", err)
	}
	return p, nil
}

// pollWithOptionsSelect fetches a poll together with its options as one
// json_agg column, so GetByID/ListPolls need one round-trip each (not a
// second query per poll, and not N+1 across a list page) — admin traffic
// is a handful of users (docs/ai/01-stack.md), but there's no reason to
// pay N+1 when a single query does it.
const pollWithOptionsSelect = `
	SELECT p.id, p.question, p.state, p.scheduled_at, p.voting_window_seconds,
	       p.closes_at, p.opened_at, p.closed_at, p.created_at, p.updated_at,
	       COALESCE(
	           (SELECT json_agg(json_build_object('ordinal', o.ordinal, 'label', o.label) ORDER BY o.ordinal)
	            FROM poll_options o WHERE o.poll_id = p.id),
	           '[]'
	       ) AS options
	FROM polls p
`

type optionJSON struct {
	Ordinal int    `json:"ordinal"`
	Label   string `json:"label"`
}

// scannable is the subset of pgx.Row/pgx.Rows this package needs — both
// satisfy it, so scanPollWithOptions works for a single QueryRow result
// and for each row of a Query result without duplicating the Scan list.
type scannable interface {
	Scan(dest ...any) error
}

func scanPollWithOptions(row scannable) (domain.Poll, error) {
	var p domain.Poll
	var optionsRaw []byte
	if err := row.Scan(
		&p.ID, &p.Question, &p.State, &p.ScheduledAt, &p.VotingWindowSeconds,
		&p.ClosesAt, &p.OpenedAt, &p.ClosedAt, &p.CreatedAt, &p.UpdatedAt,
		&optionsRaw,
	); err != nil {
		return domain.Poll{}, err
	}

	var opts []optionJSON
	if err := json.Unmarshal(optionsRaw, &opts); err != nil {
		return domain.Poll{}, fmt.Errorf("unmarshal options: %w", err)
	}
	p.Options = make([]domain.PollOption, len(opts))
	for i, o := range opts {
		p.Options[i] = domain.PollOption{Ordinal: o.Ordinal, Label: o.Label}
	}
	return p, nil
}

// pgxQuerier is the subset of *pgxpool.Pool and pgx.Tx this package
// needs. Reading through this interface lets getPollByID run either
// against the pool (GetByID) or against an in-flight transaction
// (TransitionPoll, which must re-read the row it just updated within the
// same transaction, not via a second pool connection).
type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// getPollByID returns pgx.ErrNoRows (wrapped, so errors.Is still matches)
// if the poll doesn't exist — callers translate that to 404.
func getPollByID(ctx context.Context, q pgxQuerier, id uuid.UUID) (domain.Poll, error) {
	row := q.QueryRow(ctx, pollWithOptionsSelect+" WHERE p.id = $1", id)
	p, err := scanPollWithOptions(row)
	if err != nil {
		return domain.Poll{}, fmt.Errorf("get poll: %w", err)
	}
	return p, nil
}

func (s *PollStore) GetByID(ctx context.Context, id uuid.UUID) (domain.Poll, error) {
	return getPollByID(ctx, s.db, id)
}

// ListByStateOrID returns every poll whose state is in states, unioned
// with every poll whose id is in ids — used by internal/cache's refresh
// loop to pull in both "newly relevant" polls (state filter) and
// "already cached, re-check for a state change" polls (id filter) in
// one query. ids/states may be empty; an empty array parameter simply
// matches nothing on that side of the OR.
//
// IDs and states are passed as text/uuid arrays cast in SQL rather than
// relying on pgx's array codec for []uuid.UUID or a custom string-based
// slice type — []string round-trips through pgx unambiguously, so this
// sidesteps that question entirely instead of assuming an answer.
func (s *PollStore) ListByStateOrID(ctx context.Context, states []domain.PollState, ids []uuid.UUID) ([]domain.Poll, error) {
	stateStrs := make([]string, len(states))
	for i, st := range states {
		stateStrs[i] = string(st)
	}
	idStrs := make([]string, len(ids))
	for i, id := range ids {
		idStrs[i] = id.String()
	}

	rows, err := s.db.Query(ctx,
		pollWithOptionsSelect+" WHERE p.state = ANY($1::text[]) OR p.id = ANY($2::text[]::uuid[])",
		stateStrs, idStrs,
	)
	if err != nil {
		return nil, fmt.Errorf("list polls by state or id: %w", err)
	}
	defer rows.Close()

	var polls []domain.Poll
	for rows.Next() {
		p, err := scanPollWithOptions(rows)
		if err != nil {
			return nil, fmt.Errorf("scan poll: %w", err)
		}
		polls = append(polls, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate polls: %w", err)
	}
	return polls, nil
}

// ListPollsFilter is nil-State for "no filter"; Limit/Offset are always
// applied (the handler defaults them per api/openapi.yaml: limit default
// 20, max 100).
type ListPollsFilter struct {
	State  *domain.PollState
	Limit  int
	Offset int
}

// ListPolls returns one page plus the total count matching the filter
// (ignoring Limit/Offset), for api/openapi.yaml's {items, total}.
func (s *PollStore) ListPolls(ctx context.Context, f ListPollsFilter) ([]domain.Poll, int, error) {
	where := ""
	args := []any{}
	if f.State != nil {
		where = "WHERE p.state = $1"
		args = append(args, *f.State)
	}

	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM polls p "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count polls: %w", err)
	}

	args = append(args, f.Limit, f.Offset)
	query := fmt.Sprintf("%s %s ORDER BY p.created_at DESC LIMIT $%d OFFSET $%d",
		pollWithOptionsSelect, where, len(args)-1, len(args))

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list polls: %w", err)
	}
	defer rows.Close()

	var polls []domain.Poll
	for rows.Next() {
		p, err := scanPollWithOptions(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan poll: %w", err)
		}
		polls = append(polls, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate polls: %w", err)
	}
	return polls, total, nil
}

// TransitionPoll applies one FSM edge (docs/ai/01-stack.md:
// draft -> scheduled -> open -> closed). The current state is read under
// `FOR UPDATE` so two concurrent transition requests for the same poll
// can't both see the pre-transition state and both believe their edge is
// legal.
//
// Returns (wrapped) pgx.ErrNoRows if the poll doesn't exist, or a
// *domain.TransitionError if the edge isn't allowed from the poll's
// current state — callers map the former to 404 and the latter to 409.
func (s *PollStore) TransitionPoll(ctx context.Context, id uuid.UUID, req domain.TransitionRequest) (domain.Poll, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return domain.Poll{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	var current domain.PollState
	if err := tx.QueryRow(ctx, `SELECT state FROM polls WHERE id = $1 FOR UPDATE`, id).Scan(&current); err != nil {
		return domain.Poll{}, fmt.Errorf("lock poll: %w", err)
	}

	if !domain.AllowedTransition(current, req.To) {
		return domain.Poll{}, &domain.TransitionError{From: current, To: req.To}
	}

	switch req.To {
	case domain.PollScheduled:
		// scheduled_at is the only input to closes_at (computed by the
		// polls_set_closes_at trigger) — opening later doesn't move it,
		// so a poll's voting window is fixed and cacheable from the
		// moment it's scheduled.
		_, err = tx.Exec(ctx, `UPDATE polls SET state = $1, scheduled_at = $2 WHERE id = $3`,
			domain.PollScheduled, req.ScheduledAt, id)
	case domain.PollOpen:
		_, err = tx.Exec(ctx, `UPDATE polls SET state = $1, opened_at = now() WHERE id = $2`,
			domain.PollOpen, id)
	case domain.PollClosed:
		_, err = tx.Exec(ctx, `UPDATE polls SET state = $1, closed_at = now() WHERE id = $2`,
			domain.PollClosed, id)
	}
	if err != nil {
		return domain.Poll{}, fmt.Errorf("update poll state: %w", err)
	}

	detail := map[string]any{"from": string(current), "to": string(req.To)}
	if err := insertAuditLog(ctx, tx, "poll.transition", &id, detail); err != nil {
		return domain.Poll{}, fmt.Errorf("audit log: %w", err)
	}

	p, err := getPollByID(ctx, tx, id)
	if err != nil {
		return domain.Poll{}, fmt.Errorf("reload poll: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Poll{}, fmt.Errorf("commit tx: %w", err)
	}
	return p, nil
}

// ListSnapshotCandidateIDs returns polls the snapshotter should still be
// writing snapshots for: currently open, or closed more recently than
// `since`. The latter half is review finding B2 — without it, a poll
// closing between one snapshot tick and the next would lose whatever
// votes arrived in that last second, since the tick that would have
// captured them runs after the poll has already left `open` and (before
// this) would have already been dropped from consideration.
func (s *PollStore) ListSnapshotCandidateIDs(ctx context.Context, since time.Time) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id FROM polls
		WHERE state = 'open'
		   OR (state = 'closed' AND closed_at IS NOT NULL AND closed_at > $1)
	`, since)
	if err != nil {
		return nil, fmt.Errorf("list snapshot candidates: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan snapshot candidate: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListDuePolls returns polls still `scheduled` whose scheduled_at has
// already arrived — review finding B4: without something transitioning
// these to open automatically, a human has to click "open" in the exact
// second the spot airs, defeating the point of scheduling in advance.
func (s *PollStore) ListDuePolls(ctx context.Context, now time.Time) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id FROM polls WHERE state = 'scheduled' AND scheduled_at <= $1
	`, now)
	if err != nil {
		return nil, fmt.Errorf("list due polls: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan due poll: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListExpiredOpenPolls returns open polls whose fixed voting window has ended.
// The schedule worker transitions them to closed so admin state and snapshot
// retention do not depend on an operator clicking close after every broadcast.
func (s *PollStore) ListExpiredOpenPolls(ctx context.Context, now time.Time) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id FROM polls WHERE state = 'open' AND closes_at <= $1
	`, now)
	if err != nil {
		return nil, fmt.Errorf("list expired open polls: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan expired open poll: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
