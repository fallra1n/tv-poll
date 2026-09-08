package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// execer is satisfied by both pgx.Tx and *pgxpool.Pool — insertAuditLog
// runs inside a transaction from CreatePoll/TransitionPoll, but
// PollStore.InsertAuditLog (for standalone actions like
// /internal/warmup, which isn't part of any larger transaction) needs
// to call it directly against the pool.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// insertAuditLog records one admin action. detail is passed straight
// through as the query argument for the jsonb column — pgx v5's JSON
// codec marshals arbitrary Go values (here, a map) via encoding/json when
// no faster path applies, so no manual json.Marshal is needed here.
func insertAuditLog(ctx context.Context, db execer, action string, pollID *uuid.UUID, detail map[string]any) error {
	_, err := db.Exec(ctx, `
		INSERT INTO admin_audit_log (action, poll_id, detail)
		VALUES ($1, $2, $3)
	`, action, pollID, detail)
	return err
}

// InsertAuditLog is the exported form, for actions not already inside a
// transaction (e.g. /internal/warmup).
func (s *PollStore) InsertAuditLog(ctx context.Context, action string, pollID *uuid.UUID, detail map[string]any) error {
	return insertAuditLog(ctx, s.db, action, pollID, detail)
}
