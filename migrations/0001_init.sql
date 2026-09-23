-- VoteGuard initial schema
-- Guarantees: UNIQUE(voter_token_id), append-only votes via trigger

CREATE TABLE polls (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL CHECK (status IN ('DRAFT', 'OPEN', 'CLOSED')),
    opens_at    TIMESTAMPTZ,
    closes_at   TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE poll_options (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    poll_id       UUID NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
    label         TEXT NOT NULL,
    display_order INT NOT NULL DEFAULT 0
);

CREATE INDEX poll_options_poll_id_idx ON poll_options(poll_id);

CREATE TABLE voting_tokens (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    poll_id               UUID NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
    token_hash            TEXT NOT NULL,
    issued_to_identity_ref TEXT NOT NULL,
    redeemed_at           TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (poll_id, token_hash)
);

CREATE INDEX voting_tokens_poll_hash_idx ON voting_tokens(poll_id, token_hash);

CREATE TABLE votes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    poll_id         UUID NOT NULL REFERENCES polls(id),
    option_id       UUID NOT NULL REFERENCES poll_options(id),
    voter_token_id  UUID NOT NULL REFERENCES voting_tokens(id),
    cast_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (voter_token_id)
);

CREATE INDEX votes_poll_option_idx ON votes(poll_id, option_id);

CREATE TABLE poll_result_cache (
    poll_id     UUID NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
    option_id   UUID NOT NULL REFERENCES poll_options(id) ON DELETE CASCADE,
    vote_count  BIGINT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (poll_id, option_id)
);

CREATE TABLE reconciliation_runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    poll_id         UUID NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
    option_id       UUID NOT NULL REFERENCES poll_options(id) ON DELETE CASCADE,
    cached_count    BIGINT NOT NULL,
    verified_count  BIGINT NOT NULL,
    drift           BIGINT NOT NULL,
    checked_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX reconciliation_runs_poll_idx ON reconciliation_runs(poll_id, checked_at DESC);

-- Append-only enforcement on votes (LedgerLine-style)
CREATE OR REPLACE FUNCTION reject_vote_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'votes is append-only: % not allowed', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER votes_immutable
    BEFORE UPDATE OR DELETE ON votes
    FOR EACH ROW
    EXECUTE PROCEDURE reject_vote_mutation();
