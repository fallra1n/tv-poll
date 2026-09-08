-- +goose Up

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE polls (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    question               TEXT NOT NULL CHECK (length(question) BETWEEN 1 AND 500),
    state                  TEXT NOT NULL DEFAULT 'draft'
                               CHECK (state IN ('draft', 'scheduled', 'open', 'closed')),

    -- scheduled_at is the single input to closes_at (see trigger below).
    -- opened_at/closed_at record when the poll actually transitioned,
    -- which can differ from the schedule (manual override, prescaling
    -- jitter, early close) — see docs/ai/04-data-model.md §1.1.
    scheduled_at           TIMESTAMPTZ,
    voting_window_seconds  INTEGER NOT NULL DEFAULT 300 CHECK (voting_window_seconds >= 30),
    closes_at              TIMESTAMPTZ,
    opened_at              TIMESTAMPTZ,
    closed_at              TIMESTAMPTZ,

    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- closes_at is computed by a trigger, not GENERATED ALWAYS AS: Postgres
-- rejects `timestamptz + interval` in a generated column expression
-- (STABLE, not IMMUTABLE — verified against real Postgres 16, see
-- docs/ai/04-data-model.md §1.1). The trigger gives the same
-- can't-desync guarantee without that restriction, and fires on
-- scheduled_at now (not opens_at, which no longer exists as a separate
-- input — GET /v1/polls/{id} needs opens_at/closes_at fixed and knowable
-- as soon as a poll is scheduled, for CDN cacheability).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION polls_set_closes_at() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.scheduled_at IS NOT NULL THEN
        NEW.closes_at := NEW.scheduled_at + NEW.voting_window_seconds * INTERVAL '1 second';
    ELSE
        NEW.closes_at := NULL;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER polls_closes_at_trigger
    BEFORE INSERT OR UPDATE OF scheduled_at, voting_window_seconds ON polls
    FOR EACH ROW EXECUTE FUNCTION polls_set_closes_at();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION polls_set_updated_at() RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER polls_updated_at_trigger
    BEFORE UPDATE ON polls
    FOR EACH ROW EXECUTE FUNCTION polls_set_updated_at();

CREATE INDEX polls_state_idx ON polls (state);

CREATE TABLE poll_options (
    poll_id     UUID NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
    ordinal     SMALLINT NOT NULL CHECK (ordinal >= 1),
    label       TEXT NOT NULL CHECK (length(label) BETWEEN 1 AND 200),
    PRIMARY KEY (poll_id, ordinal)
);

CREATE TABLE poll_result_snapshots (
    poll_id                 UUID NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
    snapshot_at             TIMESTAMPTZ NOT NULL,
    total_accepted          BIGINT NOT NULL,
    rejected_duplicate      BIGINT NOT NULL DEFAULT 0,
    rejected_rate_limited   BIGINT NOT NULL DEFAULT 0,
    option_counts           JSONB NOT NULL,
    sampling_enabled        BOOLEAN NOT NULL DEFAULT false,
    sampling_rate           INTEGER,
    PRIMARY KEY (poll_id, snapshot_at)
);

CREATE TABLE admin_audit_log (
    id            BIGSERIAL PRIMARY KEY,
    occurred_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    action        TEXT NOT NULL,
    poll_id       UUID REFERENCES polls(id),
    detail        JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX admin_audit_log_poll_id_idx ON admin_audit_log (poll_id);

-- +goose Down

DROP TABLE IF EXISTS admin_audit_log;
DROP TABLE IF EXISTS poll_result_snapshots;
DROP TABLE IF EXISTS poll_options;
DROP TRIGGER IF EXISTS polls_updated_at_trigger ON polls;
DROP TRIGGER IF EXISTS polls_closes_at_trigger ON polls;
DROP FUNCTION IF EXISTS polls_set_updated_at();
DROP FUNCTION IF EXISTS polls_set_closes_at();
DROP TABLE IF EXISTS polls;
