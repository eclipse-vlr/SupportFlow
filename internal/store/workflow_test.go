package store

import (
	"crypto/rand"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"supportflow/internal/ticket"
)

func workflowTicket(t *testing.T, s *Store, title string) ticket.Ticket {
	t.Helper()
	_, _, err := s.Create(t.Context(), ticket.Input{Title: title, Message: "Проблема оплаты, код 100%_готово"}, rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.Claim(t.Context(), 3, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(t.Context(), claimed, validAnalysis(), ticket.Usage{Model: "test-model"}); err != nil {
		t.Fatal(err)
	}
	item, err := s.Get(t.Context(), claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestClosureRequiresCurrentApprovalAndProtectsReply(t *testing.T) {
	s := testStore(t, 10)
	item := workflowTicket(t, s, "Закрытие")
	if _, err := s.SetClosed(t.Context(), item.ID, true, item.Version); !errors.Is(err, ticket.ErrApprovalRequired) {
		t.Fatalf("close draft: %v", err)
	}
	approved := ticket.Review{Category: "billing", DraftReply: "Подтверждённый ответ", Approved: true}
	item, err := s.Review(t.Context(), item.ID, approved, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetClosed(t.Context(), item.ID, true, 0); !errors.Is(err, ticket.ErrVersionConflict) {
		t.Fatalf("stale closure: %v", err)
	}
	closed, err := s.SetClosed(t.Context(), item.ID, true, item.Version)
	if err != nil || closed.ClosedAt == nil || closed.Status != "completed" || closed.ReviewRevision != 1 {
		t.Fatalf("close: %+v %v", closed, err)
	}
	retry, err := s.SetClosed(t.Context(), item.ID, true, item.Version)
	if err != nil || retry.Version != closed.Version || !retry.ClosedAt.Equal(*closed.ClosedAt) {
		t.Fatalf("retry closure: %+v %v", retry, err)
	}
	if _, err = s.Review(t.Context(), item.ID, ticket.Review{Category: "access", DraftReply: "Новый черновик"}, 1); !errors.Is(err, ticket.ErrClosed) {
		t.Fatalf("editing closed ticket: %v", err)
	}
	reopened, err := s.SetClosed(t.Context(), item.ID, false, closed.Version)
	if err != nil || reopened.ClosedAt != nil || *reopened.Review != approved || !reflect.DeepEqual(reopened.Analysis, item.Analysis) {
		t.Fatalf("reopen: %+v %v", reopened, err)
	}
	if _, err = s.Review(t.Context(), item.ID, ticket.Review{Category: "access", DraftReply: "Новый черновик"}, 1); err != nil {
		t.Fatal(err)
	}
	history, err := s.History(t.Context(), item.ID, 0, 10)
	if err != nil || len(history) != 2 {
		t.Fatalf("history: %+v %v", history, err)
	}
}

func TestFeedbackPreservesAnalysisReviewAndClosure(t *testing.T) {
	s := testStore(t, 10)
	item := workflowTicket(t, s, "Оценка")
	item, err := s.Review(t.Context(), item.ID, ticket.Review{Category: "billing", DraftReply: "Ответ оператора", Approved: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	item, err = s.SetClosed(t.Context(), item.ID, true, item.Version)
	if err != nil {
		t.Fatal(err)
	}
	feedback := ticket.Feedback{Rating: "rewritten", Reason: "Не учёл дату платежа"}
	saved, err := s.SetFeedback(t.Context(), item.ID, feedback, item.Version)
	if err != nil || saved.Feedback == nil || *saved.Feedback != feedback || saved.Version != item.Version+1 || saved.ReviewRevision != item.ReviewRevision ||
		!reflect.DeepEqual(saved.Analysis, item.Analysis) || *saved.Review != *item.Review || !saved.ClosedAt.Equal(*item.ClosedAt) {
		t.Fatalf("feedback: %+v %v", saved, err)
	}
	retry, err := s.SetFeedback(t.Context(), item.ID, feedback, item.Version)
	if err != nil || retry.Version != saved.Version {
		t.Fatalf("retry feedback: %+v %v", retry, err)
	}
	if _, err = s.SetFeedback(t.Context(), item.ID, ticket.Feedback{Rating: "useful"}, item.Version); !errors.Is(err, ticket.ErrVersionConflict) {
		t.Fatalf("stale feedback: %v", err)
	}
	loaded, err := New(s.pool, 10).Get(t.Context(), item.ID)
	if err != nil || !reflect.DeepEqual(loaded.Feedback, saved.Feedback) || loaded.ClosedAt == nil {
		t.Fatalf("reload: %+v %v", loaded, err)
	}
	history, err := s.History(t.Context(), item.ID, 0, 10)
	if err != nil || len(history) != 1 {
		t.Fatalf("feedback rewrote history: %+v %v", history, err)
	}
}

func TestClosingAndEditingCannotProduceClosedUnapprovedReply(t *testing.T) {
	s := testStore(t, 10)
	item := workflowTicket(t, s, "Одновременные действия")
	item, err := s.Review(t.Context(), item.ID, ticket.Review{Category: "billing", DraftReply: "Одобрено", Approved: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	group.Go(func() { <-start; _, err := s.SetClosed(t.Context(), item.ID, true, item.Version); results <- err })
	group.Go(func() {
		<-start
		_, err := s.Review(t.Context(), item.ID, ticket.Review{Category: "billing", DraftReply: "Правка"}, item.ReviewRevision)
		results <- err
	})
	close(start)
	group.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ticket.ErrClosed) && !errors.Is(err, ticket.ErrVersionConflict) {
			t.Errorf("unexpected concurrent outcome: %v", err)
		}
	}
	current, err := s.Get(t.Context(), item.ID)
	if err != nil || successes != 1 || (current.ClosedAt != nil && !current.Review.Approved) {
		t.Fatalf("illegal state: %+v err=%v successes=%d", current, err, successes)
	}
}

func TestSearchAndOperatorFilters(t *testing.T) {
	s := testStore(t, 10)
	unreviewed := workflowTicket(t, s, "Двойное Списание")
	approved := workflowTicket(t, s, "Подтверждено")
	approved, err := s.Review(t.Context(), approved.ID, ticket.Review{Category: "access", DraftReply: "Ответ", Approved: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := workflowTicket(t, s, "Закрыто")
	closed, err = s.Review(t.Context(), closed.ID, ticket.Review{Category: "billing", DraftReply: "Ответ", Approved: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetClosed(t.Context(), closed.ID, true, closed.Version); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Create(t.Context(), ticket.Input{Title: "В очереди", Message: "Без анализа"}, rand.Text()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		filter ticket.ListFilter
		count  int
		id     string
	}{
		{ticket.ListFilter{View: "needs_review"}, 1, unreviewed.ID},
		{ticket.ListFilter{View: "closed"}, 1, closed.ID},
		{ticket.ListFilter{View: "open"}, 3, ""},
		{ticket.ListFilter{Search: "двойное списание"}, 1, unreviewed.ID},
		{ticket.ListFilter{Search: "100%_готово"}, 3, ""},
		{ticket.ListFilter{Search: "100%_несуществующее"}, 0, ""},
		{ticket.ListFilter{Category: "access", Status: "completed", View: "open", Search: "подтверждено"}, 1, approved.ID},
		{ticket.ListFilter{View: "needs_review", Status: "pending"}, 0, ""},
		{ticket.ListFilter{Search: "' OR true --"}, 0, ""},
	} {
		items, err := s.List(t.Context(), tc.filter, 10, 0)
		if err != nil || len(items) != tc.count || (tc.id != "" && items[0].ID != tc.id) {
			t.Errorf("List(%+v): %+v %v", tc.filter, items, err)
		}
	}
	first, err := s.List(t.Context(), ticket.ListFilter{}, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.List(t.Context(), ticket.ListFilter{}, 2, 2)
	if err != nil || len(first) != 2 || len(second) != 2 || first[0].ID == second[0].ID || first[1].ID == second[1].ID {
		t.Fatalf("pagination: %+v %+v %v", first, second, err)
	}
}

func TestWorkflowMigrationPreservesVersionTwoData(t *testing.T) {
	s := testDatabase(t, 10)
	if _, err := s.pool.Exec(t.Context(), schema+reviewHistorySchema); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), `CREATE TABLE schema_migrations (version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now()); INSERT INTO schema_migrations(version) VALUES (1),(2)`); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := s.pool.QueryRow(t.Context(), `INSERT INTO tickets(title,message,idempotency_key,status,analysis,review,review_revision)
VALUES('Old','Old','v2','completed','{"category":"billing","summary":"Old","priority":"medium","priority_reason":"Old","draft_reply":"Original"}',
'{"category":"billing","draft_reply":"Approved","approved":true}',4) RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	item, err := s.Get(t.Context(), id)
	if err != nil || item.Version != 0 || item.ClosedAt != nil || item.Feedback != nil || item.ReviewRevision != 4 || item.Review.DraftReply != "Approved" || item.Analysis.DraftReply != "Original" {
		t.Fatalf("migration changed legacy data: %+v %v", item, err)
	}
}
