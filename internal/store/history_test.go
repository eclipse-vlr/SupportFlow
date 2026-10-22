package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"supportflow/internal/ticket"
)

func completedTicket(t *testing.T, s *Store) ticket.Ticket {
	t.Helper()
	_, _, err := s.Create(t.Context(), ticket.Input{Title: "Оплата", Message: "Двойное списание"}, "history")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.Claim(t.Context(), 3, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(t.Context(), claimed, validAnalysis(), ticket.Usage{}); err != nil {
		t.Fatal(err)
	}
	return claimed
}

func TestHistoryPreservesDraftsAndApprovals(t *testing.T) {
	s := testStore(t, 10)
	item := completedTicket(t, s)
	draft := ticket.Review{Category: "access", DraftReply: "Уточните дату списания."}
	first, err := s.Review(t.Context(), item.ID, draft, 0)
	if err != nil || first.ReviewRevision != 1 {
		t.Fatalf("Review(first draft) = %+v, %v, want revision 1", first, err)
	}
	// A lost HTTP response can be retried without creating another history event.
	replayed, err := s.Review(t.Context(), item.ID, draft, 0)
	if err != nil || replayed.ReviewRevision != 1 {
		t.Fatalf("Review(retry) = %+v, %v, want revision 1", replayed, err)
	}
	approval := draft
	approval.Approved = true
	if _, err = s.Review(t.Context(), item.ID, approval, 1); err != nil {
		t.Fatal(err)
	}
	draft.DraftReply = "Уточните дату и сумму списания."
	latest, err := s.Review(t.Context(), item.ID, draft, 2)
	if err != nil || latest.ReviewRevision != 3 || latest.Review.Approved || *latest.Analysis != validAnalysis() {
		t.Fatalf("Review(after approval) = %+v, %v, want draft revision 3 and original analysis", latest, err)
	}
	if _, err = s.Review(t.Context(), item.ID, approval, 1); !errors.Is(err, ticket.ErrReviewConflict) {
		t.Errorf("Review(stale revision) = %v, want ErrReviewConflict", err)
	}
	history, err := s.History(t.Context(), item.ID, 0, 2)
	if err != nil || len(history) != 2 {
		t.Fatalf("History(first page) = %+v, %v, want 2 entries", history, err)
	}
	if history[0].Revision != 3 || history[0].Kind != "draft_saved" || history[1].Revision != 2 || history[1].Kind != "approved" || history[1].Review != approval {
		t.Errorf("History(first page) = %+v, want revisions 3 and 2 with saved snapshots", history)
	}
	older, err := s.History(t.Context(), item.ID, 2, 2)
	if err != nil || len(older) != 1 || older[0].Revision != 1 || older[0].Review.DraftReply != "Уточните дату списания." || older[0].CreatedAt.IsZero() {
		t.Errorf("History(older page) = %+v, %v, want original saved draft", older, err)
	}
}

func TestConcurrentReviewsCannotLoseChanges(t *testing.T) {
	s := testStore(t, 10)
	item := completedTicket(t, s)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for index := range 8 {
		wg.Go(func() {
			review := ticket.Review{Category: "billing", DraftReply: string(rune('A' + index))}
			_, err := s.Review(t.Context(), item.ID, review, 0)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ticket.ErrReviewConflict) {
			t.Errorf("Review(concurrent) = %v, want success or revision conflict", err)
		}
	}
	history, err := s.History(t.Context(), item.ID, 0, 10)
	if winners != 1 || err != nil || len(history) != 1 {
		t.Errorf("concurrent Review: winners=%d history=%+v err=%v, want one winner and one event", winners, history, err)
	}
}

func TestHistoryFailureRollsBackLatestReview(t *testing.T) {
	s := testStore(t, 10)
	item := completedTicket(t, s)
	first := ticket.Review{Category: "billing", DraftReply: "Первый ответ"}
	if _, err := s.Review(t.Context(), item.ID, first, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), "ALTER TABLE ticket_review_history ADD CONSTRAINT reject_second_review CHECK (revision < 2)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Review(t.Context(), item.ID, ticket.Review{Category: "other", DraftReply: "Не должен сохраниться"}, 1); err == nil {
		t.Fatal("Review(history insert fails) returned nil, want transaction failure")
	}
	current, err := s.Get(t.Context(), item.ID)
	if err != nil || current.ReviewRevision != 1 || current.Review == nil || *current.Review != first {
		t.Errorf("Get(after failed history insert) = %+v, %v, want first review and revision 1", current, err)
	}
}

func TestMigrationAdoptsLegacyReviewsOnce(t *testing.T) {
	s := testDatabase(t, 10)
	ctx := t.Context()
	if _, err := s.pool.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	var id string
	err := s.pool.QueryRow(ctx, `INSERT INTO tickets (title,message,idempotency_key,status,analysis,review)
VALUES ('Legacy','Legacy','legacy','completed',
'{"category":"billing","summary":"Original","priority":"medium","priority_reason":"One user","draft_reply":"Original reply"}',
'{"category":"access","draft_reply":"Saved before migration","approved":true}') RETURNING id::text`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	current, err := s.Get(ctx, id)
	if err != nil || current.ReviewRevision != 1 || current.Review == nil || current.Review.DraftReply != "Saved before migration" || current.Analysis.DraftReply != "Original reply" {
		t.Fatalf("Get(migrated legacy review) = %+v, %v, want preserved analysis and review", current, err)
	}
	history, err := s.History(ctx, id, 0, 10)
	if err != nil || len(history) != 1 || history[0].Kind != "imported" || history[0].Review != *current.Review {
		t.Errorf("History(after repeated migration) = %+v, %v, want one imported snapshot", history, err)
	}
}
