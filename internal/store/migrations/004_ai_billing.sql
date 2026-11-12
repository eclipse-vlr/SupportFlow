ALTER TABLE tickets ADD COLUMN usage_unknown boolean NOT NULL DEFAULT true;
CREATE TABLE ai_calls (
    id text PRIMARY KEY,
    ticket_id text NOT NULL,
    attempt integer NOT NULL,
    model text NOT NULL,
    prompt_version text NOT NULL DEFAULT 'support-v2',
    request_id text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','recorded','unknown')),
    reserved bigint NOT NULL CHECK (reserved > 0),
    cost bigint NOT NULL DEFAULT 0 CHECK (cost >= 0),
    input_rate bigint NOT NULL,
    output_rate bigint NOT NULL,
    input_tokens bigint,
    output_tokens bigint,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz
);
CREATE INDEX ai_calls_ticket ON ai_calls(ticket_id);
CREATE INDEX ai_calls_created ON ai_calls(created_at);
CREATE TABLE ai_budget_policy (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    daily bigint NOT NULL, per_ticket bigint NOT NULL,
    input_rate bigint NOT NULL, output_rate bigint NOT NULL
);
