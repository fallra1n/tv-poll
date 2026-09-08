package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// insertAuditLog records one admin action. detail is passed straight
// through as the query argument for the jsonb column — pgx v5's JSON
// codec marshals arbitrary Go values (here, a map) via encoding/json when
// no faster path applies, so no manual json.Marshal is needed here.
func insertAuditLog(ctx context.Context, tx pgx.Tx, action string, pollID *uuid.UUID, detail map[string]any) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO admin_audit_log (action, poll_id, detail)
		VALUES ($1, $2, $3)
	`, action, pollID, detail)
	return err
}
