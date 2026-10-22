package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"supportflow/internal/ticket"
)

//go:embed schema.sql
var schema string

type Store struct {
	pool      *pgxpool.Pool
	maxQueued int
}

func New(pool *pgxpool.Pool, maxQueued int) *Store { return &Store{pool: pool, maxQueued: maxQueued} }

// Migrate creates the initial schema under a transaction-scoped migration lock.
// Subsequent schema versions must use explicit versioned migrations.
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(78345901)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, schema); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const columns = `id::text, title, message, status, attempts, analysis, review,
model, input_tokens, output_tokens, last_error, created_at, updated_at`

func scan(row pgx.Row) (ticket.Ticket, error) {
	var t ticket.Ticket
	var analysis, review []byte
	err := row.Scan(&t.ID, &t.Title, &t.Message, &t.Status, &t.Attempts, &analysis, &review,
		&t.Usage.Model, &t.Usage.InputTokens, &t.Usage.OutputTokens, &t.LastError, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ticket.ErrNotFound
	}
	if err != nil {
		return t, err
	}
	if analysis != nil {
		if err = json.Unmarshal(analysis, &t.Analysis); err != nil {
			return t, err
		}
	}
	if review != nil {
		if err = json.Unmarshal(review, &t.Review); err != nil {
			return t, err
		}
	}
	return t, nil
}

// Create serializes queue admission so simultaneous requests cannot overfill it.
// The same idempotency key and payload always resolve to the original ticket.
func (s *Store) Create(ctx context.Context, input ticket.Input, key string) (ticket.Ticket, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ticket.Ticket{}, false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(78345902)"); err != nil {
		return ticket.Ticket{}, false, err
	}
	existing, err := scan(tx.QueryRow(ctx, "SELECT "+columns+" FROM tickets WHERE idempotency_key=$1", key))
	if err == nil {
		if existing.Input != input {
			return ticket.Ticket{}, false, ticket.ErrConflict
		}
		return existing, false, tx.Commit(ctx)
	}
	if !errors.Is(err, ticket.ErrNotFound) {
		return ticket.Ticket{}, false, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM tickets WHERE status IN ('pending','processing')").Scan(&count); err != nil {
		return ticket.Ticket{}, false, err
	}
	if count >= s.maxQueued {
		return ticket.Ticket{}, false, ticket.ErrQueueFull
	}
	t, err := scan(tx.QueryRow(ctx, "INSERT INTO tickets (title,message,idempotency_key) VALUES ($1,$2,$3) RETURNING "+columns, input.Title, input.Message, key))
	if err != nil {
		return t, false, err
	}
	return t, true, tx.Commit(ctx)
}

func (s *Store) Get(ctx context.Context, id string) (ticket.Ticket, error) {
	return scan(s.pool.QueryRow(ctx, "SELECT "+columns+" FROM tickets WHERE id=$1", id))
}

func (s *Store) List(ctx context.Context, category, status string, limit, offset int) ([]ticket.Ticket, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+columns+` FROM tickets
WHERE ($1='' OR COALESCE(review->>'category',analysis->>'category')=$1)
AND ($2='' OR status=$2) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, category, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ticket.Ticket, 0)
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, t)
	}
	return items, rows.Err()
}

func (s *Store) Review(ctx context.Context, id string, review ticket.Review) (ticket.Ticket, error) {
	data, err := json.Marshal(review)
	if err != nil {
		return ticket.Ticket{}, err
	}
	t, err := scan(s.pool.QueryRow(ctx, "UPDATE tickets SET review=$2,updated_at=now() WHERE id=$1 AND status='completed' RETURNING "+columns, id, data))
	if !errors.Is(err, ticket.ErrNotFound) {
		return t, err
	}
	if _, err = s.Get(ctx, id); err != nil {
		return ticket.Ticket{}, err
	}
	return ticket.Ticket{}, ticket.ErrConflict
}

// Claim uses row locks to give one worker a lease. attempts is a fencing token:
// completion by an older lease holder cannot overwrite a newer attempt.
func (s *Store) Claim(ctx context.Context, maxAttempts int, lease time.Duration) (ticket.Ticket, error) {
	_, err := s.pool.Exec(ctx, `UPDATE tickets SET status='failed',lease_until=NULL,
last_error='attempts_exhausted',updated_at=now()
WHERE status='processing' AND lease_until<=now() AND attempts >= $1`, maxAttempts)
	if err != nil {
		return ticket.Ticket{}, err
	}
	return scan(s.pool.QueryRow(ctx, `WITH next AS (
SELECT id FROM tickets WHERE attempts<$1 AND
((status='pending' AND available_at<=now()) OR (status='processing' AND lease_until<=now()))
ORDER BY available_at,created_at FOR UPDATE SKIP LOCKED LIMIT 1)
UPDATE tickets t SET status='processing', attempts=t.attempts+1,
lease_until=now()+($2 * interval '1 millisecond'),updated_at=now()
FROM next WHERE t.id=next.id RETURNING `+qualifiedColumns(), maxAttempts, lease.Milliseconds()))
}

func qualifiedColumns() string {
	return `t.id::text,t.title,t.message,t.status,t.attempts,t.analysis,t.review,
t.model,t.input_tokens,t.output_tokens,t.last_error,t.created_at,t.updated_at`
}

func (s *Store) Complete(ctx context.Context, t ticket.Ticket, a ticket.Analysis, u ticket.Usage) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	result, err := s.pool.Exec(ctx, `UPDATE tickets SET status='completed',analysis=$3,
model=$4,input_tokens=input_tokens+$5,output_tokens=output_tokens+$6,
last_error='',lease_until=NULL,updated_at=now()
WHERE id=$1 AND attempts=$2 AND status='processing' AND lease_until>now()`, t.ID, t.Attempts, data, u.Model, u.InputTokens, u.OutputTokens)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ticket.ErrConflict
	}
	return nil
}

func (s *Store) Fail(ctx context.Context, t ticket.Ticket, code string, retry bool, delay time.Duration, u ticket.Usage) error {
	status := "failed"
	if retry {
		status = "pending"
	}
	result, err := s.pool.Exec(ctx, `UPDATE tickets SET status=$3,last_error=$4,lease_until=NULL,
available_at=now()+($5 * interval '1 millisecond'),updated_at=now(),
model=$6,input_tokens=input_tokens+$7,output_tokens=output_tokens+$8
WHERE id=$1 AND attempts=$2 AND status='processing' AND lease_until>now()`, t.ID, t.Attempts, status, code, delay.Milliseconds(), u.Model, u.InputTokens, u.OutputTokens)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ticket.ErrConflict
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("database unavailable: %w", err)
	}
	return nil
}
