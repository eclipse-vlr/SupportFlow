ALTER TABLE tickets ADD COLUMN closed_at timestamptz;
ALTER TABLE tickets ADD COLUMN feedback jsonb;
ALTER TABLE tickets ADD COLUMN version bigint NOT NULL DEFAULT 0 CHECK (version >= 0);
ALTER TABLE tickets ADD CONSTRAINT tickets_closed_requires_approval CHECK (
    closed_at IS NULL OR (status = 'completed' AND review IS NOT NULL AND review @> '{"approved":true}')
);
ALTER TABLE tickets ADD CONSTRAINT tickets_feedback_requires_analysis CHECK (
    feedback IS NULL OR (status = 'completed' AND jsonb_typeof(feedback) = 'object'
    AND feedback ? 'rating' AND feedback->>'rating' IN ('useful', 'rewritten'))
);
