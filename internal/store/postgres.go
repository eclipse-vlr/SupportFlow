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

//go:embed migrations/002_review_history.sql
var reviewHistorySchema string

//go:embed migrations/003_operator_workflow.sql
var operatorWorkflowSchema string

type Store struct {
	pool      *pgxpool.Pool
	maxQueued int
}

func New(pool *pgxpool.Pool, maxQueued int) *Store { return &Store{pool: pool, maxQueued: maxQueued} }

// Migrate applies versioned changes under a transaction-scoped migration lock.
// Databases created before migration tracking are adopted as version 1.
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(78345901)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	var current int
	if err = tx.QueryRow(ctx, "SELECT COALESCE(max(version),0) FROM schema_migrations").Scan(&current); err != nil {
		return err
	}
	migrations := []string{schema, reviewHistorySchema, operatorWorkflowSchema}
	if current > len(migrations) {
		return errors.New("database schema is newer than this application")
	}
	for index, migration := range migrations {
		version := index + 1
		if version <= current {
			continue
		}
		if _, err = tx.Exec(ctx, migration); err != nil {
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

const columns = `id::text, title, message, status, attempts, analysis, review,
model, input_tokens, output_tokens, last_error, created_at, updated_at, review_revision,version,closed_at,feedback`

func scan(row pgx.Row) (ticket.Ticket, error) {
	var t ticket.Ticket
	var analysis, review, feedback []byte
	err := row.Scan(&t.ID, &t.Title, &t.Message, &t.Status, &t.Attempts, &analysis, &review,
		&t.Usage.Model, &t.Usage.InputTokens, &t.Usage.OutputTokens, &t.LastError, &t.CreatedAt, &t.UpdatedAt, &t.ReviewRevision, &t.Version, &t.ClosedAt, &feedback)
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
	if feedback != nil {
		if err = json.Unmarshal(feedback, &t.Feedback); err != nil {
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

func (s *Store) List(ctx context.Context, filter ticket.ListFilter, limit, offset int) ([]ticket.Ticket, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+columns+` FROM tickets
WHERE ($1='' OR COALESCE(review->>'category',analysis->>'category')=$1)
AND ($2='' OR status=$2)
AND ($3='' OR strpos(lower(title || E'\n' || message),lower($3))>0)
AND ($4='' OR ($4='open' AND closed_at IS NULL) OR ($4='closed' AND closed_at IS NOT NULL)
OR ($4='needs_review' AND closed_at IS NULL AND status='completed' AND NOT COALESCE(review @> '{"approved":true}',false)))
ORDER BY created_at DESC,id DESC LIMIT $5 OFFSET $6`, filter.Category, filter.Status, filter.Search, filter.View, limit, offset)
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

// Review commits the latest snapshot and its history event together. A stale
// revision cannot replace another edit; retrying an identical save is a no-op.
func (s *Store) Review(ctx context.Context, id string, review ticket.Review, expectedRevision int64) (ticket.Ticket, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ticket.Ticket{}, err
	}
	defer tx.Rollback(ctx)
	current, err := scan(tx.QueryRow(ctx, "SELECT "+columns+" FROM tickets WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return ticket.Ticket{}, err
	}
	if current.Status != "completed" {
		return ticket.Ticket{}, ticket.ErrConflict
	}
	if current.ClosedAt != nil {
		return ticket.Ticket{}, ticket.ErrClosed
	}
	unchanged := current.Review != nil && *current.Review == review
	if expectedRevision != current.ReviewRevision {
		if unchanged && expectedRevision == current.ReviewRevision-1 {
			return current, tx.Commit(ctx)
		}
		return ticket.Ticket{}, ticket.ErrReviewConflict
	}
	if unchanged {
		return current, tx.Commit(ctx)
	}
	data, err := json.Marshal(review)
	if err != nil {
		return ticket.Ticket{}, err
	}
	t, err := scan(tx.QueryRow(ctx, "UPDATE tickets SET review=$2,review_revision=review_revision+1,version=version+1,updated_at=now() WHERE id=$1 RETURNING "+columns, id, data))
	if err != nil {
		return t, err
	}
	kind := "draft_saved"
	if review.Approved {
		kind = "approved"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ticket_review_history (ticket_id,revision,kind,review)
VALUES ($1,$2,$3,$4)`, id, t.ReviewRevision, kind, data); err != nil {
		return ticket.Ticket{}, err
	}
	return t, tx.Commit(ctx)
}

// History returns saved versions in descending order using a revision cursor.
func (s *Store) History(ctx context.Context, id string, before int64, limit int) ([]ticket.ReviewEvent, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM tickets WHERE id=$1)", id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ticket.ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT revision,kind,review,created_at FROM ticket_review_history
WHERE ticket_id=$1 AND ($2::bigint=0 OR revision<$2) ORDER BY revision DESC LIMIT $3`, id, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ticket.ReviewEvent, 0)
	for rows.Next() {
		var event ticket.ReviewEvent
		var data []byte
		if err = rows.Scan(&event.Revision, &event.Kind, &data, &event.CreatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &event.Review); err != nil {
			return nil, err
		}
		items = append(items, event)
	}
	return items, rows.Err()
}

// Claim uses row locks to give one worker a lease. attempts is a fencing token:
// completion by an older lease holder cannot overwrite a newer attempt.
func (s *Store) Claim(ctx context.Context, maxAttempts int, lease time.Duration) (ticket.Ticket, error) {
	_, err := s.pool.Exec(ctx, `UPDATE tickets SET status='failed',lease_until=NULL,
last_error='attempts_exhausted',version=version+1,updated_at=now()
WHERE status='processing' AND lease_until<=now() AND attempts >= $1`, maxAttempts)
	if err != nil {
		return ticket.Ticket{}, err
	}
	return scan(s.pool.QueryRow(ctx, `WITH next AS (
SELECT id FROM tickets WHERE attempts<$1 AND
((status='pending' AND available_at<=now()) OR (status='processing' AND lease_until<=now()))
ORDER BY available_at,created_at FOR UPDATE SKIP LOCKED LIMIT 1)
UPDATE tickets t SET status='processing', attempts=t.attempts+1,
lease_until=now()+($2 * interval '1 millisecond'),version=t.version+1,updated_at=now()
FROM next WHERE t.id=next.id RETURNING `+qualifiedColumns(), maxAttempts, lease.Milliseconds()))
}

func qualifiedColumns() string {
	return `t.id::text,t.title,t.message,t.status,t.attempts,t.analysis,t.review,
t.model,t.input_tokens,t.output_tokens,t.last_error,t.created_at,t.updated_at,t.review_revision,t.version,t.closed_at,t.feedback`
}

func (s *Store) Complete(ctx context.Context, t ticket.Ticket, a ticket.Analysis, u ticket.Usage) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	result, err := s.pool.Exec(ctx, `UPDATE tickets SET status='completed',analysis=$3,
model=$4,input_tokens=input_tokens+$5,output_tokens=output_tokens+$6,
last_error='',lease_until=NULL,version=version+1,updated_at=now()
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
available_at=now()+($5 * interval '1 millisecond'),version=version+1,updated_at=now(),
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
