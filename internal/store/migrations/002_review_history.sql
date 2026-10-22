ALTER TABLE tickets ADD COLUMN review_revision bigint NOT NULL DEFAULT 0 CHECK (review_revision >= 0);

CREATE TABLE ticket_review_history (
    ticket_id uuid NOT NULL REFERENCES tickets(id),
    revision bigint NOT NULL CHECK (revision > 0),
    kind text NOT NULL CHECK (kind IN ('draft_saved', 'approved', 'imported')),
    review jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (ticket_id, revision)
);

-- Earlier versions only kept the latest review. Preserve that snapshot without
-- claiming that older edits or their authors can be reconstructed.
INSERT INTO ticket_review_history (ticket_id, revision, kind, review)
SELECT id, 1, 'imported', review FROM tickets WHERE review IS NOT NULL;
UPDATE tickets SET review_revision = 1 WHERE review IS NOT NULL;
