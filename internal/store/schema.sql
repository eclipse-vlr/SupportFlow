CREATE TABLE IF NOT EXISTS tickets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key text NOT NULL UNIQUE,
    title text NOT NULL,
    message text NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    analysis jsonb,
    review jsonb,
    model text NOT NULL DEFAULT '',
    input_tokens bigint NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens bigint NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    last_error text NOT NULL DEFAULT '',
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((status = 'processing') = (lease_until IS NOT NULL)),
    CHECK (status <> 'completed' OR analysis IS NOT NULL),
    CHECK (review IS NULL OR status = 'completed')
);
CREATE INDEX IF NOT EXISTS tickets_queue_idx ON tickets (available_at, created_at) WHERE status IN ('pending', 'processing');
CREATE INDEX IF NOT EXISTS tickets_created_idx ON tickets (created_at DESC, id DESC);
