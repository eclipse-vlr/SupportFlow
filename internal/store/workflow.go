package store

import (
	"context"
	"encoding/json"

	"supportflow/internal/ticket"
)

// SetClosed serializes closing with review edits. It cannot close a draft that
// changed after the operator loaded it, and closed replies cannot be edited.
func (s *Store) SetClosed(ctx context.Context, id string, closed bool, expectedVersion int64) (ticket.Ticket, error) {
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
	unchanged := (current.ClosedAt != nil) == closed
	if expectedVersion != current.Version {
		if unchanged && expectedVersion == current.Version-1 {
			return current, tx.Commit(ctx)
		}
		return ticket.Ticket{}, ticket.ErrVersionConflict
	}
	if unchanged {
		return current, tx.Commit(ctx)
	}
	if closed && (current.Review == nil || !current.Review.Approved) {
		return ticket.Ticket{}, ticket.ErrApprovalRequired
	}
	updated, err := scan(tx.QueryRow(ctx, `UPDATE tickets SET closed_at=CASE WHEN $2 THEN now() ELSE NULL END,
version=version+1,updated_at=now() WHERE id=$1 RETURNING `+columns, id, closed))
	if err != nil {
		return ticket.Ticket{}, err
	}
	return updated, tx.Commit(ctx)
}

// SetFeedback changes only the assessment of the original analysis. The saved
// operator reply and its review history are never rewritten by a rating.
func (s *Store) SetFeedback(ctx context.Context, id string, feedback ticket.Feedback, expectedVersion int64) (ticket.Ticket, error) {
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
	unchanged := current.Feedback != nil && *current.Feedback == feedback
	if expectedVersion != current.Version {
		if unchanged && expectedVersion == current.Version-1 {
			return current, tx.Commit(ctx)
		}
		return ticket.Ticket{}, ticket.ErrVersionConflict
	}
	if unchanged {
		return current, tx.Commit(ctx)
	}
	data, err := json.Marshal(feedback)
	if err != nil {
		return ticket.Ticket{}, err
	}
	updated, err := scan(tx.QueryRow(ctx, `UPDATE tickets SET feedback=$2,version=version+1,updated_at=now()
WHERE id=$1 RETURNING `+columns, id, data))
	if err != nil {
		return ticket.Ticket{}, err
	}
	return updated, tx.Commit(ctx)
}
